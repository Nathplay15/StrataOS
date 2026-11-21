#!/bin/bash
# Installs the smart photos runtime: ONNX Runtime, NumPy and Pillow from
# Debian, and the models, downloaded once and pinned by SHA-256:
#   - YuNet face detector (OpenCV Zoo, MIT, 0.2 MB)
#   - SFace face embeddings (OpenCV Zoo, Apache 2.0, 37 MB)
#   - OpenAI CLIP ViT-B/32, 8-bit ONNX export of Xenova (MIT): image
#     encoder 85 MB, text encoder 62 MB, tokenizer files 1.3 MB
# On a PC with an Intel GPU or NPU, the OpenVINO build of ONNX Runtime (MIT,
# PyPI wheel, 81 MB, pinned) goes into /usr/local/lib/strata/photoml/runtime; the
# worker uses its provider and falls back to the CPU.
# Everything runs on the NAS: no picture ever leaves it.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends python3-onnxruntime python3-numpy python3-pil curl ca-certificates

DIR=/usr/local/lib/strata/photoml/models
install -d -m 755 /usr/local/lib/strata/photoml "$DIR"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() { # url name sha256
  if [ -f "$DIR/$2" ] && echo "$3  $DIR/$2" | sha256sum -c --quiet 2>/dev/null; then
    echo ">> $2 already there"
    return
  fi
  echo ">> downloading $2"
  curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$tmp/$2" "$1"
  echo "$3  $tmp/$2" | sha256sum -c --quiet || { echo "checksum mismatch for $2: download refused" >&2; exit 1; }
  install -m 644 "$tmp/$2" "$DIR/$2"
}
HF=https://huggingface.co
CLIP=$HF/Xenova/clip-vit-base-patch32/resolve/6b7c523703ae26e39be4b03774966f8571c7f523
fetch $HF/opencv/face_detection_yunet/resolve/3cc26e7f1014a5ee5d74a42acee58bafc9d0a310/face_detection_yunet_2023mar.onnx face_detection_yunet_2023mar.onnx 8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4
fetch $HF/opencv/face_recognition_sface/resolve/3d7082438a6e4551e840c9b2bb60b71e8da4b524/face_recognition_sface_2021dec.onnx face_recognition_sface_2021dec.onnx 0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79
fetch $CLIP/onnx/vision_model_quantized.onnx vision_model_quantized.onnx 583fd1110a514667812fee7d684952aaf82a99b959760c8d7dca7e0ab9839299
fetch $CLIP/onnx/text_model_quantized.onnx text_model_quantized.onnx 73baab855d406190da9faa498cfedf65f15cf309f4cc7385b7b032e6d08e5c3a
fetch $CLIP/vocab.json vocab.json 5047b556ce86ccaf6aa22b3ffccfc52d391ea4accdab9c2f2407da5b742d4363
fetch $CLIP/merges.txt merges.txt 9fd691f7c8039210e0fced15865466c65820d09b63988b0174bfe25de299051a
fetch $HF/opencv/face_detection_yunet/resolve/3cc26e7f1014a5ee5d74a42acee58bafc9d0a310/LICENSE LICENSE-yunet c83b8120c50ccbd4c4f96edf53141bdd566ebb8f8e9227e415326aa1b1aba958
fetch $HF/opencv/face_recognition_sface/resolve/3d7082438a6e4551e840c9b2bb60b71e8da4b524/LICENSE LICENSE-sface cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30

# Intel GPU (render node of vendor 0x8086) or NPU (/dev/accel): OpenVINO.
intel=0
for v in /sys/class/drm/renderD*/device/vendor; do [ "$(cat "$v" 2>/dev/null)" = 0x8086 ] && intel=1; done
ls /dev/accel/accel* >/dev/null 2>&1 && intel=1
if [ "$intel" = 1 ] && [ "$(dpkg --print-architecture)" = amd64 ] && python3 -c 'import sys; sys.exit(sys.version_info[:2] != (3, 13))'; then
  apt-get install -y --no-install-recommends python3-venv
  W=onnxruntime_openvino-1.24.1-cp313-cp313-manylinux_2_28_x86_64.whl
  echo ">> Intel GPU/NPU found: downloading the OpenVINO runtime"
  curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$tmp/$W" "https://files.pythonhosted.org/packages/08/07/f225999919f56506b603aaa3ff837ad563ab26f86906ed7fa7e5abcd849e/$W"
  echo "2c3bb73e68ac27f4891af8a595c1faf574ec68b772e6583c90a0b997a1822782  $tmp/$W" | sha256sum -c --quiet || { echo "checksum mismatch: download refused" >&2; exit 1; }
  rm -rf /usr/local/lib/strata/photoml/runtime
  python3 -m venv --system-site-packages /usr/local/lib/strata/photoml/runtime
  /usr/local/lib/strata/photoml/runtime/bin/pip install --quiet --no-deps --no-index "$tmp/$W"
  chmod -R a+rX /usr/local/lib/strata/photoml/runtime
  apt-get install -y --no-install-recommends intel-opencl-icd || true
fi
echo ">> Smart photos runtime and models are installed (enable them in Photos → Smart features)"
