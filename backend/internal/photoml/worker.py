#!/usr/bin/python3
# Smart photos worker of StrataOS: face detection (YuNet), face embeddings
# (SFace) and CLIP image/text embeddings (OpenAI CLIP ViT-B/32), run with
# ONNX Runtime on the NAS itself. The daemon starts it as the unprivileged
# sandbox user, at the lowest priority, and talks to it over stdin/stdout:
#
#   request:  one JSON line, then "len" raw bytes when the line has "len"
#   answer:   one JSON line
#
#   {"op": "hello"}                               -> {"providers": [...], "provider": "..."}
#   {"op": "image", "len": n, "faces": true, "clip": true} + JPEG bytes
#       -> {"w": 1600, "h": 1200, "faces": [{"box": [x, y, w, h], "score": s, "emb": "<base64 int8>"}], "clip": "<base64 int8>"}
#   {"op": "text", "texts": ["a photo of a dog"]} -> {"embs": ["<base64 int8>", ...]}
#
# Boxes are fractions of the picture; embeddings are L2-normalized and
# quantized to int8 (x 127). It never reads files other than its models.
import base64
import html
import io
import json
import os
import re
import sys

import numpy as np
import onnxruntime as ort
from PIL import Image

Image.MAX_IMAGE_PIXELS = 80_000_000
MODELS = sys.argv[1] if len(sys.argv) > 1 else "/usr/local/lib/strata/photoml/models"
THREADS = int(sys.argv[2]) if len(sys.argv) > 2 else 1
ort.set_default_logger_severity(3)

# Accelerators first, the CPU last; STRATA_ML_PROVIDER=cpu forces the CPU.
PREFERRED = ["CUDAExecutionProvider", "ROCMExecutionProvider", "OpenVINOExecutionProvider", "CoreMLExecutionProvider", "CPUExecutionProvider"]
available = ort.get_available_providers()
providers = [p for p in PREFERRED if p in available]
if os.environ.get("STRATA_ML_PROVIDER") == "cpu" or not providers:
    providers = ["CPUExecutionProvider"]
used = {"provider": "CPUExecutionProvider"}

_sessions = {}


def session(name):
    s = _sessions.get(name)
    if s is None:
        opts = ort.SessionOptions()
        opts.intra_op_num_threads = max(1, THREADS)
        opts.inter_op_num_threads = 1
        opts.log_severity_level = 3
        path = os.path.join(MODELS, name)
        try:
            s = ort.InferenceSession(path, sess_options=opts, providers=providers)
        except Exception:
            s = ort.InferenceSession(path, sess_options=opts, providers=["CPUExecutionProvider"])
        used["provider"] = s.get_providers()[0]
        _sessions[name] = s
    return s


def q8(v):
    v = np.asarray(v, dtype=np.float32).reshape(-1)
    n = np.linalg.norm(v)
    if n > 0:
        v = v / n
    return base64.b64encode(np.clip(np.round(v * 127), -127, 127).astype(np.int8).tobytes()).decode()


# ---- Faces: YuNet (detection, 5 landmarks) and SFace (128-d embedding) ----

DET_SIZE = 640
REF = np.array([[38.2946, 51.6963], [73.5318, 51.5014], [56.0252, 71.7366], [41.5493, 92.3655], [70.7299, 92.2041]], dtype=np.float64)


def nms(boxes, scores, thr):
    order = scores.argsort()[::-1]
    keep = []
    while order.size:
        i = order[0]
        keep.append(i)
        x1 = np.maximum(boxes[i, 0], boxes[order[1:], 0])
        y1 = np.maximum(boxes[i, 1], boxes[order[1:], 1])
        x2 = np.minimum(boxes[i, 0] + boxes[i, 2], boxes[order[1:], 0] + boxes[order[1:], 2])
        y2 = np.minimum(boxes[i, 1] + boxes[i, 3], boxes[order[1:], 1] + boxes[order[1:], 3])
        inter = np.maximum(0, x2 - x1) * np.maximum(0, y2 - y1)
        union = boxes[i, 2] * boxes[i, 3] + boxes[order[1:], 2] * boxes[order[1:], 3] - inter
        order = order[1:][inter / np.maximum(union, 1e-6) <= thr]
    return keep


def detect(img, min_score=0.8):
    w, h = img.size
    scale = DET_SIZE / max(w, h)
    sw, sh = max(1, round(w * scale)), max(1, round(h * scale))
    canvas = Image.new("RGB", (DET_SIZE, DET_SIZE))
    canvas.paste(img.resize((sw, sh), Image.BILINEAR), (0, 0))
    x = np.asarray(canvas, dtype=np.float32)[:, :, ::-1].transpose(2, 0, 1)[None]  # BGR, 0-255
    out = dict(zip([o.name for o in session("face_detection_yunet_2023mar.onnx").get_outputs()], session("face_detection_yunet_2023mar.onnx").run(None, {"input": np.ascontiguousarray(x)})))
    boxes, scores, kps = [], [], []
    for s in (8, 16, 32):
        cols = DET_SIZE // s
        cls = out["cls_%d" % s][0, :, 0]
        obj = out["obj_%d" % s][0, :, 0]
        score = np.sqrt(np.clip(cls, 0, 1) * np.clip(obj, 0, 1))
        idx = np.where(score >= min_score)[0]
        if not idx.size:
            continue
        r, c = idx // cols, idx % cols
        b = out["bbox_%d" % s][0, idx]
        k = out["kps_%d" % s][0, idx]
        cx, cy = (c + b[:, 0]) * s, (r + b[:, 1]) * s
        bw, bh = np.exp(b[:, 2]) * s, np.exp(b[:, 3]) * s
        boxes.append(np.stack([cx - bw / 2, cy - bh / 2, bw, bh], 1))
        scores.append(score[idx])
        pts = np.empty((idx.size, 10), dtype=np.float32)
        pts[:, 0::2] = (k[:, 0::2] + c[:, None]) * s
        pts[:, 1::2] = (k[:, 1::2] + r[:, None]) * s
        kps.append(pts)
    if not boxes:
        return []
    boxes, scores, kps = np.concatenate(boxes) / scale, np.concatenate(scores), np.concatenate(kps) / scale
    return [(boxes[i], float(scores[i]), kps[i].reshape(5, 2)) for i in nms(boxes, scores, 0.3)[:50]]


def similarity(src, dst):
    """Least-squares similarity transform (Umeyama) mapping src points to dst."""
    n = src.shape[0]
    ms, md = src.mean(0), dst.mean(0)
    s, d = src - ms, dst - md
    cov = d.T @ s / n
    u, sig, vt = np.linalg.svd(cov)
    e = np.eye(2)
    if np.linalg.det(u) * np.linalg.det(vt) < 0:
        e[1, 1] = -1
    r = u @ e @ vt
    scale = np.trace(np.diag(sig) @ e) / s.var(0).sum()
    m = np.eye(3)
    m[:2, :2] = scale * r
    m[:2, 2] = md - scale * r @ ms
    return m


def embed_face(img, pts):
    inv = np.linalg.inv(similarity(pts.astype(np.float64), REF))  # 112x112 -> picture
    crop = img.transform((112, 112), Image.AFFINE, data=tuple(inv[:2].reshape(-1)), resample=Image.BILINEAR)
    x = np.asarray(crop, dtype=np.float32).transpose(2, 0, 1)[None]  # RGB, 0-255
    return session("face_recognition_sface_2021dec.onnx").run(None, {"data": np.ascontiguousarray(x)})[0]


# ---- CLIP ViT-B/32 ----

MEAN = np.array([0.48145466, 0.4578275, 0.40821073], dtype=np.float32)
STD = np.array([0.26862954, 0.26130258, 0.27577711], dtype=np.float32)


def clip_image(img):
    w, h = img.size
    s = 224 / min(w, h)
    img = img.resize((max(224, round(w * s)), max(224, round(h * s))), Image.BICUBIC)
    w, h = img.size
    l, t = (w - 224) // 2, (h - 224) // 2
    x = (np.asarray(img.crop((l, t, l + 224, t + 224)), dtype=np.float32) / 255 - MEAN) / STD
    return session("vision_model_quantized.onnx").run(None, {"pixel_values": np.ascontiguousarray(x.transpose(2, 0, 1)[None])})[0]


def bytes_to_unicode():
    bs = list(range(ord("!"), ord("~") + 1)) + list(range(ord("¡"), ord("¬") + 1)) + list(range(ord("®"), ord("ÿ") + 1))
    cs = bs[:]
    n = 0
    for b in range(256):
        if b not in bs:
            bs.append(b)
            cs.append(256 + n)
            n += 1
    return dict(zip(bs, map(chr, cs)))


class Tokenizer:
    """The BPE tokenizer of CLIP (lower case, 77 tokens with start and end)."""

    PAT = re.compile(r"<\|startoftext\|>|<\|endoftext\|>|'s|'t|'re|'ve|'m|'ll|'d|[^\W\d_]+|\d|(?:[^\s\w]|_)+", re.IGNORECASE)

    def __init__(self):
        with open(os.path.join(MODELS, "vocab.json"), encoding="utf-8") as f:
            self.vocab = json.load(f)
        with open(os.path.join(MODELS, "merges.txt"), encoding="utf-8") as f:
            merges = [tuple(l.split()) for l in f.read().split("\n")[1:] if l.strip()]
        self.ranks = {m: i for i, m in enumerate(merges)}
        self.byte = bytes_to_unicode()
        self.cache = {}

    def bpe(self, token):
        if token in self.cache:
            return self.cache[token]
        word = list(token[:-1]) + [token[-1] + "</w>"]
        while len(word) > 1:
            pairs = [(self.ranks.get((word[i], word[i + 1]), 1 << 30), i) for i in range(len(word) - 1)]
            rank, i = min(pairs)
            if rank == 1 << 30:
                break
            a, b = word[i], word[i + 1]
            out, j = [], 0
            while j < len(word):
                if j < len(word) - 1 and word[j] == a and word[j + 1] == b:
                    out.append(a + b)
                    j += 2
                else:
                    out.append(word[j])
                    j += 1
            word = out
        self.cache[token] = word
        return word

    def encode(self, text, length=77):
        text = re.sub(r"\s+", " ", html.unescape(html.unescape(text))).strip().lower()
        ids = [self.vocab["<|startoftext|>"]]
        for tok in self.PAT.findall(text):
            tok = "".join(self.byte[b] for b in tok.encode("utf-8"))
            ids += [self.vocab[t] for t in self.bpe(tok) if t in self.vocab]
        ids = ids[: length - 1] + [self.vocab["<|endoftext|>"]]
        return ids


_tok = []


def clip_texts(texts):
    if not _tok:
        _tok.append(Tokenizer())
    ids = [_tok[0].encode(t) for t in texts]
    n = max(len(i) for i in ids)
    eos = _tok[0].vocab["<|endoftext|>"]
    x = np.array([i + [eos] * (n - len(i)) for i in ids], dtype=np.int64)
    return session("text_model_quantized.onnx").run(None, {"input_ids": x})[0]


# ---- Loop ----

def handle(req, data):
    op = req.get("op")
    if op == "hello":
        return {"providers": providers, "provider": used["provider"], "onnxruntime": ort.__version__}
    if op == "image":
        img = Image.open(io.BytesIO(data))
        img.draft("RGB", (1600, 1600))
        img = img.convert("RGB")
        w, h = img.size
        out = {"w": w, "h": h, "faces": []}
        if req.get("faces"):
            for box, score, pts in detect(img):
                x, y, bw, bh = (float(v) for v in box)
                if bw < 24 or bh < 24:  # too small to recognize anyone
                    continue
                out["faces"].append({"box": [max(0.0, x / w), max(0.0, y / h), min(1.0, bw / w), min(1.0, bh / h)], "score": round(score, 3), "emb": q8(embed_face(img, pts))})
        if req.get("clip"):
            out["clip"] = q8(clip_image(img))
        out["provider"] = used["provider"]
        return out
    if op == "text":
        texts = [str(t)[:300] for t in req.get("texts", [])][:256]
        if not texts:
            return {"embs": []}
        embs = []
        for i in range(0, len(texts), 32):
            embs += [q8(e) for e in clip_texts(texts[i:i + 32])]
        return {"embs": embs}
    return {"error": "unknown operation"}


def main():
    inp, out = sys.stdin.buffer, sys.stdout
    while True:
        line = inp.readline()
        if not line:
            return
        try:
            req = json.loads(line)
            n = int(req.get("len", 0))
            if n < 0 or n > 64 << 20:
                raise ValueError("invalid length")
            data = inp.read(n) if n else b""
            if len(data) != n:
                return
            ans = handle(req, data)
        except Exception as e:  # one bad picture never stops the worker
            ans = {"error": "%s: %s" % (type(e).__name__, str(e)[:300])}
        out.write(json.dumps(ans) + "\n")
        out.flush()


if __name__ == "__main__":
    main()
