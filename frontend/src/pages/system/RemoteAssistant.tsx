// Remote access assistant: what to reach from outside, the method that fits
// (ranked by the NAS: api/h_remote_check.go, gate.Recommend), its setup with
// the existing sections, then "Is it reachable from outside?".
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, CircleAlert, CircleCheck, CircleX, Compass, Info, Play, RefreshCw, X } from "lucide-react";
import { api } from "../../lib/api";
import { useAction } from "../../lib/hooks";
import { cx } from "../../lib/format";
import { revealElement } from "../../lib/intent";
import { srv, tr } from "../../lib/i18n";
import { Badge, Button, Card, ErrorBox, Field, Input, Modal, Notice, Spinner, Switch } from "../../components/ui";
import { AppDomains, CloudflareTunnel, Tailscale } from "./RemoteExtras";

type Goal = "ui" | "files" | "photos" | "links" | "phones";
type Method = "tunnel" | "tailscale" | "wireguard" | "domain";
type Option = { method: Method; score: number; recommended: boolean; suitable: boolean; configured: boolean; covers: Goal[]; missing: Goal[]; notes: string[] };
type Facts = { nat: string; natMethod?: string; publicIp?: string; routerIp?: string; tunnel: { connected: boolean; configured: boolean }; tailscale: { installed: boolean; loggedIn: boolean; dnsName?: string }; wireguard: boolean; proxy: boolean; domains: string[] | null };
type Advice = { goals: Goal[]; options: Option[]; facts: Facts };
type Finding = { step: string; level: "ok" | "warn" | "fail" | "info"; code: string; detail?: string };
type Check = { host: string; address?: string; verdict: "reachable" | "unreachable" | "partial" | "unknown"; findings: Finding[]; publicIp?: string; nat: string };

const goals = (): { id: Goal; label: string; hint: string }[] => [
  { id: "ui", label: tr("Manage the NAS"), hint: tr("The StrataOS web interface from anywhere") },
  { id: "files", label: tr("My files"), hint: tr("Files, folder sync, network drives") },
  { id: "photos", label: tr("My photos"), hint: tr("Photos and videos, phone backup") },
  { id: "links", label: tr("Share with anyone"), hint: tr("Share links and file requests opened by people without an account") },
  { id: "phones", label: tr("Family phones"), hint: tr("The StrataOS app on several phones and laptops") },
];

const methodName = (m: Method) => ({ tunnel: tr("Cloudflare Tunnel"), tailscale: tr("Tailscale"), wireguard: tr("WireGuard VPN"), domain: tr("Your domain with port forwarding") })[m];
const methodText = (m: Method) => ({
  tunnel: tr("Cloudflare carries the traffic to the NAS: nothing to open on the router, works behind any Internet box. Needs a free Cloudflare account and a domain name managed by Cloudflare."),
  tailscale: tr("A private network between your devices: install Tailscale on the NAS and on each phone or computer, sign in with the same account. Nothing is exposed to the Internet."),
  wireguard: tr("The NAS's own VPN server: no outside service, but a UDP port forwarded on the router and the WireGuard app on each device."),
  domain: tr("Your domain name points to your router, which forwards ports 80 and 443 to the NAS; certificates come from Let's Encrypt. No outside service in the way, but the NAS is directly exposed."),
})[m];

const noteText = (n: string) => ({
  "no-port": tr("No port to open on the router"),
  "port-udp": tr("One UDP port to forward on the router"),
  "port-tcp": tr("Ports 80 and 443 to forward on the router"),
  "account-cloudflare": tr("Free Cloudflare account"),
  "account-tailscale": tr("Free Tailscale account"),
  "no-account": tr("No outside account"),
  "domain-needed": tr("A domain name"),
  "public": tr("Reachable by anyone: keep two-factor authentication on"),
  "private": tr("Only your own devices get in"),
  "web-only": tr("Web only: network drives (SMB) need a VPN"),
  "upload-limit": tr("Cloudflare limits each upload request to 100 MB on free plans"),
  "app-per-device": tr("An app to install on each device"),
  "smb-ok": tr("Network drives (SMB) work too"),
  "cgnat-blocks": tr("Not possible here: your Internet provider shares your public address (CGNAT)"),
  "cgnat-unknown": tr("Check that your provider gives you a public address (no CGNAT)"),
})[n] ?? n;

const natText = (n: string) => ({
  direct: tr("Your router has the public address: port forwarding can work."),
  cgnat: tr("Your Internet provider shares your public address with other customers (CGNAT): ports cannot be forwarded. Use Cloudflare Tunnel or Tailscale, or ask your provider for a public IPv4 address."),
  double: tr("There is a second router in front of yours: forward the ports on both, or put the provider's box in bridge mode."),
  unknown: tr("The router did not say (UPnP and NAT-PMP are off or not supported): the NAS cannot tell whether your provider uses CGNAT."),
})[n] ?? n;

const findingText = (code: string) => ({
  "dns-ok": tr("The name is known on the Internet"),
  "dns-other-ip": tr("The name points to another address than your public address: update the DNS record (or the dynamic DNS)"),
  "dns-private": tr("The name points to a private address: nobody outside can use it"),
  "dns-local-only": tr("Only your network knows this name: public DNS servers do not"),
  "dns-public-unchecked": tr("Public DNS servers could not be asked (no Internet access?)"),
  "dns-missing": tr("This name does not exist: check its spelling and the DNS record"),
  "nat-cgnat": tr("Carrier-grade NAT: port forwarding cannot work on this line"),
  "nat-double": tr("Double NAT: forward the ports on both routers"),
  "nat-direct": tr("The router has the public address"),
  "nat-unknown": tr("The NAT type could not be read from the router"),
  "tcp-ok": tr("The port answers"),
  "tcp-refused": tr("The port is closed: check the port forwarding on the router and that the service runs"),
  "tcp-timeout": tr("No answer on the port: blocked by a firewall, or no port forwarding"),
  "tcp-hairpin": tr("The NAS could not loop back through the router (many routers cannot): this does not prove the port is closed. Test from a phone on mobile data."),
  "tcp-skipped": tr("Not tested"),
  "tested-from-inside": tr("Tested from the NAS through your router: to be sure, open it from a phone on mobile data."),
  "tls-ok": tr("Valid certificate"),
  "tls-untrusted": tr("The certificate is not trusted by browsers (self-signed): get one from Let's Encrypt in App domains"),
  "tls-expired": tr("The certificate has expired"),
  "tls-name": tr("The certificate is for another name"),
  "tls-error": tr("No secure connection possible (is it HTTPS on this port?)"),
  "http-this-nas": tr("This NAS answered"),
  "http-other": tr("Something answered, but not this NAS's interface"),
  "http-ok": tr("The service answered"),
  "http-error": tr("The service answered with an error"),
  "http-gateway": tr("The gateway answered that the service is unreachable (tunnel down, or wrong local address in the tunnel)"),
  "http-none": tr("No answer from the web server"),
})[code] ?? code;

const levelIcon = (l: Finding["level"]) => l === "ok" ? <CircleCheck size={15} className="text-ok" /> : l === "fail" ? <CircleX size={15} className="text-danger" /> : l === "warn" ? <CircleAlert size={15} className="text-warn" /> : <Info size={15} className="text-muted" />;

/** "Is it reachable from outside?" for a name (tunnel or domain). */
export function ReachTest({ method, facts }: { method: Method; facts?: Facts }) {
  const [f, setF] = useState({ host: facts?.domains?.[0] ?? "", port: 443, ui: true, http: false });
  const run = useAction(() => api.post<Check>("/api/remote/check", { ...f, via: method === "tunnel" ? "tunnel" : "domain" }));
  const r = run.data;
  const verdict = r && ({
    reachable: <Badge tone="ok" dot>{tr("Reachable from outside")}</Badge>,
    partial: <Badge tone="warn" dot>{tr("Reachable, with a problem")}</Badge>,
    unreachable: <Badge tone="danger" dot>{tr("Not reachable from outside")}</Badge>,
    unknown: <Badge dot>{tr("Could not be confirmed from the NAS")}</Badge>,
  })[r.verdict];
  return (
    <div className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-[1fr_7rem]">
        <Field label={method === "tunnel" ? tr("Public hostname of the tunnel") : tr("Host name")}><Input className="mono" value={f.host} onChange={(e) => setF({ ...f, host: e.target.value.trim() })} placeholder="nas.example.com" /></Field>
        <Field label={tr("Port")}><Input type="number" min={1} max={65535} value={f.port} onChange={(e) => setF({ ...f, port: +e.target.value })} /></Field>
      </div>
      <Switch checked={f.ui} onChange={(v) => setF({ ...f, ui: v })} label={tr("This name opens the StrataOS interface")} hint={tr("The test then checks that this very NAS answers.")} />
      <Button variant="primary" icon={<Play size={15} />} loading={run.isPending} disabled={!f.host} onClick={() => run.mutate()}>{tr("Test")}</Button>
      <ErrorBox error={run.error} />
      {r && (
        <div className="space-y-2 rounded-xl border border-line p-3">
          <div className="flex flex-wrap items-center gap-2 text-[13px]">{verdict}{r.address && <span className="mono text-muted">{r.address}</span>}</div>
          <ul className="space-y-1.5 text-[13px]">
            {r.findings.map((x, i) => <li key={i} className="flex items-start gap-2">{levelIcon(x.level)}<span>{findingText(x.code)}{x.detail && <span className="mono ml-1 text-muted text-[12px]">({x.detail})</span>}</span></li>)}
          </ul>
        </div>
      )}
      <p className="text-[12px] text-muted">{tr("The test runs from the NAS: it asks public DNS servers (Cloudflare, Quad9), connects to the address they give, checks the certificate like a browser and asks the NAS to prove it answered. Through a tunnel the request really goes out to the Internet and back. For a name that points to your own router, the result depends on the router looping back: a phone on mobile data remains the final proof.")}</p>
    </div>
  );
}

function Setup({ method, facts, onClose }: { method: Method; facts: Facts; onClose: () => void }) {
  if (method === "tailscale") return <Tailscale />;
  if (method === "tunnel") return <CloudflareTunnel />;
  if (method === "domain") return <AppDomains />;
  return (
    <Card>
      <ol className="list-decimal space-y-1.5 pl-5 text-[13px]">
        <li>{tr("Turn on the WireGuard VPN server and save (section WireGuard VPN server).")}</li>
        <li>{tr("On your router, forward the UDP port of the VPN (51820 by default) to the NAS.")}</li>
        <li>{tr("If your public address changes, set up a dynamic DNS name and use it as the public address.")}</li>
        <li>{tr("Add a device: scan its QR code with the WireGuard app on the phone.")}</li>
      </ol>
      {facts.nat === "cgnat" && <div className="mt-3"><Notice tone="warn">{natText("cgnat")}</Notice></div>}
      <Button className="mt-3" onClick={() => { onClose(); revealElement("remote-wireguard"); }}>{tr("Go to the WireGuard section")}</Button>
    </Card>
  );
}

function Verify({ method, facts }: { method: Method; facts: Facts }) {
  if (method === "tunnel" || method === "domain") return <ReachTest method={method} facts={facts} />;
  if (method === "tailscale") {
    return (
      <div className="space-y-2 text-[13px]">
        {facts.tailscale.loggedIn ? <Badge tone="ok" dot>{tr("The NAS is in your tailnet")}</Badge> : <Badge tone="warn" dot>{tr("The NAS is not signed in to Tailscale yet")}</Badge>}
        {facts.tailscale.dnsName && <p>{tr("From a device signed in to the same tailnet, open:")} <span className="mono">http://{facts.tailscale.dnsName.replace(/\.$/, "")}:7070</span></p>}
        <p className="text-muted">{tr("Tailscale is private by design: the NAS cannot test it from the Internet. Turn Wi-Fi off on a phone with Tailscale and open the address above.")}</p>
      </div>
    );
  }
  return (
    <div className="space-y-2 text-[13px]">
      <p>{natText(facts.nat)}</p>
      {facts.publicIp && <p>{tr("Public address")}: <span className="mono">{facts.publicIp}</span></p>}
      <p className="text-muted">{tr("WireGuard uses UDP, which cannot be tested from the NAS itself. Turn Wi-Fi off on a phone, connect the WireGuard app, then check that the device shows a recent handshake in the device list.")}</p>
    </div>
  );
}

/** The assistant, opened from the Remote access page. */
export function RemoteAssistant({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [step, setStep] = useState(0);
  const [picked, setPicked] = useState<Goal[]>([]);
  const [method, setMethod] = useState<Method | null>(null);
  const [refresh, setRefresh] = useState(0);
  const advice = useQuery({
    queryKey: ["remote-assistant", picked.join(","), refresh],
    queryFn: () => api.get<Advice>(`/api/remote/assistant?goals=${picked.join(",")}${refresh ? "&refresh=1" : ""}`),
    enabled: open && step >= 1,
  });
  const close = () => { setStep(0); setMethod(null); onClose(); };
  const toggle = (g: Goal) => setPicked((p) => (p.includes(g) ? p.filter((x) => x !== g) : [...p, g]));
  const steps = [tr("Your needs"), tr("Recommendation"), tr("Setup"), tr("Test")];
  const facts = advice.data?.facts;
  const footer = (
    <>
      {step > 0 && <Button onClick={() => setStep(step - 1)}>{tr("Back")}</Button>}
      {step === 0 && <Button variant="primary" disabled={!picked.length} onClick={() => setStep(1)}>{tr("Next")}</Button>}
      {step === 1 && <Button variant="primary" disabled={!method} onClick={() => setStep(2)}>{tr("Set it up")}</Button>}
      {step === 2 && <Button variant="primary" onClick={() => { setRefresh((n) => n + 1); setStep(3); }}>{tr("Test it")}</Button>}
      {step === 3 && <Button variant="primary" onClick={close}>{tr("Completed")}</Button>}
    </>
  );
  return (
    <Modal open={open} onClose={close} wide title={<span className="flex items-center gap-2"><Compass size={17} />{tr("Remote access assistant")}</span>} footer={footer}>
      <ol className="mb-4 flex flex-wrap gap-2 text-[12px]">
        {steps.map((s, i) => <li key={s} className={cx("rounded-full px-2.5 py-1", i === step ? "bg-accent text-white" : i < step ? "bg-accent-soft text-accent" : "bg-panel-2 text-muted")}>{i + 1}. {s}</li>)}
      </ol>
      {step === 0 && (
        <div className="space-y-2">
          <p className="text-[13.5px]">{tr("What do you want to do from outside your home?")}</p>
          {goals().map((g) => (
            <button key={g.id} onClick={() => toggle(g.id)} className={cx("flex w-full items-center gap-3 rounded-xl border p-3 text-left transition", picked.includes(g.id) ? "border-accent bg-accent-soft" : "border-line hover:border-accent")}>
              <span className={cx("flex h-5 w-5 shrink-0 items-center justify-center rounded border", picked.includes(g.id) ? "border-accent bg-accent text-white" : "border-line")}>{picked.includes(g.id) && <Check size={13} />}</span>
              <span><span className="block font-medium text-[13.5px]">{g.label}</span><span className="block text-muted text-[12.5px]">{g.hint}</span></span>
            </button>
          ))}
        </div>
      )}
      {step === 1 && (advice.isLoading || !advice.data ? <><ErrorBox error={advice.error} /><Spinner label={tr("Looking at your network…")} /></> : (
        <div className="space-y-3">
          <Notice tone={facts?.nat === "cgnat" ? "warn" : "info"}>
            <div className="flex items-start justify-between gap-2">
              <span>{natText(facts!.nat)}{facts?.publicIp ? ` ${tr("Public address")}: ${facts.publicIp}.` : ""}</span>
              <button className="shrink-0 text-muted hover:text-accent" title={tr("Check again")} onClick={() => setRefresh((n) => n + 1)}><RefreshCw size={14} /></button>
            </div>
          </Notice>
          {advice.data.options.map((o) => (
            <button key={o.method} disabled={!o.suitable} onClick={() => setMethod(o.method)}
              className={cx("w-full rounded-xl border p-3 text-left transition", method === o.method ? "border-accent bg-accent-soft" : "border-line", o.suitable ? "hover:border-accent" : "opacity-60")}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{methodName(o.method)}</span>
                {o.recommended && <Badge tone="accent">{tr("Recommended")}</Badge>}
                {o.configured && <Badge tone="ok">{tr("Already set up")}</Badge>}
                {!o.suitable && <Badge tone="danger">{tr("Not suitable")}</Badge>}
              </div>
              <p className="mt-1 text-muted text-[12.5px]">{methodText(o.method)}</p>
              {o.missing.length > 0 && <p className="mt-1 flex items-center gap-1 text-warn text-[12.5px]"><X size={13} />{tr("Does not cover: {0}", o.missing.map((g) => goals().find((x) => x.id === g)?.label).join(", "))}</p>}
              <div className="mt-2 flex flex-wrap gap-1">{o.notes.map((n) => <Badge key={n} tone={n.startsWith("cgnat") ? "warn" : "neutral"}>{noteText(n)}</Badge>)}</div>
            </button>
          ))}
          <p className="text-[12px] text-muted">{tr("Several methods can be combined, for example Tailscale for your own devices and Cloudflare Tunnel for share links.")}</p>
        </div>
      ))}
      {step === 2 && method && facts && <Setup method={method} facts={facts} onClose={close} />}
      {step === 3 && method && (advice.isLoading || !facts ? <Spinner /> : <Verify method={method} facts={facts} />)}
      {advice.error && step > 1 ? <div className="mt-3 text-danger text-[12.5px]">{srv(String(advice.error))}</div> : null}
    </Modal>
  );
}
