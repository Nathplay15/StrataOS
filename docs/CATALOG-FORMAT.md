# Community app catalogs: format 1

Besides its built-in catalog, the App Center of StrataOS (0.10 and later) reads **community catalogs**: lists of apps maintained by someone else, added by an administrator in App Center → **Community catalogs**. This page describes the format, for people who publish one. An example lives in [`packaging/catalog-example/`](../packaging/catalog-example/).

StrataOS does not review community apps. It checks every app strictly before showing it, labels it "Community · not verified by StrataOS", and before an install or an update it shows exactly what the app gets on the NAS (ports, folders, privileges, devices). Anything risky needs an explicit approval, item by item.

## Where a catalog lives

A catalog is one file, `catalog.json` (UTF-8 JSON, 4 MiB at most), published either:

- at an **HTTPS address**: `https://example.org/strataos/catalog.json`. Its signature, if any, is at the same address plus `.sig` (`https://example.org/strataos/catalog.json.sig`);
- in a **Git repository**, at its root, with the signature in `catalog.json.sig` next to it: `https://codeberg.org/me/strata-apps.git`, or `git+https://…` for an address not ending in `.git`, with an optional branch or tag: `git+https://github.com/me/strata-apps#stable`. The NAS makes a shallow clone over HTTPS (no submodules, no LFS, symbolic links checked out as plain files) and reads only these two files. Git must be installed on the NAS (`apt install git`).

Plain `http://` is accepted only for an address on the NAS itself (`127.0.0.1`, `localhost`: a local mirror or a test server); the content is checked by its signature or checksum either way. Redirects to anything else than HTTPS are refused.

## Trust: signed or pinned

When adding a catalog, the administrator either:

- gives the **Ed25519 public key** of its maintainer (`ed25519:<base64 of the 32 bytes>`, or a PEM `PUBLIC KEY`). Every download must then come with a valid signature of `catalog.json` by that key; a refresh that brings a new signed version applies it, an unsigned or badly signed one is refused and the last good version is kept;
- or gives no key: the NAS shows the SHA-256 of the current `catalog.json` and the administrator **pins** the catalog to it (compare it with the one the maintainer publishes). When a refresh finds a different file, the new version waits, unused, until the administrator accepts its new checksum.

Signing with OpenSSL (3.0 or later):

```bash
openssl genpkey -algorithm ed25519 -out signing-key.pem          # once; keep it secret
openssl pkey -in signing-key.pem -pubout -outform DER | tail -c 32 | base64 | sed 's/^/ed25519:/'   # the public key to publish
openssl pkeyutl -sign -inkey signing-key.pem -rawin -in catalog.json | base64 -w0 > catalog.json.sig   # after every change
```

The signature file holds the 64-byte signature, in base64 (as above) or raw. It covers the exact bytes of `catalog.json`: sign after the last change, and publish both files together.

## The index

```json
{
  "format": 1,
  "name": "My apps",
  "description": "Apps I maintain for StrataOS.",
  "maintainer": "Jane Doe",
  "homepage": "https://example.org/strataos",
  "apps": [ … ]
}
```

| Field | | |
|---|---|---|
| `format` | required | `1`. A NAS refuses a format it does not know. |
| `name` | required | Up to 64 characters, one line. Shown as the source of each app. |
| `description`, `maintainer` | optional | Up to 500 and 100 characters. |
| `homepage` | optional | http(s) address. |
| `apps` | required | Up to 500 apps. |

Unknown fields are refused (a typo is an error, not a silently ignored setting). An app that fails its checks is left out and listed, with the reason, under the catalog in the App Center; the other apps stay available.

## An app

```json
{
  "id": "whoami",
  "name": "Whoami",
  "version": "1.10.4",
  "maintainer": "Jane Doe",
  "description": "Tiny web server that prints information about the request.",
  "category": "Development",
  "icon": "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/traefik.svg",
  "homepage": "https://github.com/traefik/whoami",
  "webPort": "WEB_PORT",
  "vars": [
    { "key": "WEB_PORT", "label": "Web port", "default": "8099", "type": "port" }
  ],
  "compose": "services:\n  whoami:\n    image: traefik/whoami:v1.10.4\n    restart: unless-stopped\n    ports:\n      - \"${WEB_PORT}:80\"\n"
}
```

| Field | | |
|---|---|---|
| `id` | required | Lowercase letters, digits and `-`, 40 at most, unique in the catalog. The NAS identifies an installed app by catalog and id. |
| `name` | required | Up to 64 characters. |
| `version` | required | Letters, digits and `. + _ -`, 32 at most. Any change of this string offers the update to the NAS that installed the app. |
| `maintainer` | required | Who maintains this app (100 characters). |
| `description`, `category` | optional | Up to 500 and 32 characters. |
| `icon`, `homepage` | optional | http(s) addresses. |
| `webPort` | optional | Key of a `port` variable: the port the **Open** button uses. |
| `vars` | optional | Up to 30 settings asked at install, written to the stack's `.env`. |
| `compose` | required | The Compose file (YAML, 64 KiB at most), with `${KEY}` references to the variables. |

### Variables

| Field | | |
|---|---|---|
| `key` | required | `^[A-Z_][A-Z0-9_]*$`. Names compose or the system read themselves are refused: `PATH`, `HOME`, `USER`, `LANG`…, and anything starting with `COMPOSE_`, `DOCKER_`, `LC_`, `XDG_`, `SYSTEMD_`, `BUILDKIT_`, `BUILDX_`. |
| `label` | required | Shown in the install form (80 characters). |
| `type` | required | `text`, `port` (1 to 65535), `path` (absolute; created at install when it is under `/srv/strata`), `password` (generated at install, at least 8 characters; no default allowed) or `device` (under `/dev`). |
| `default` | optional | Default value. Values never contain quotes, `$`, backslashes or line breaks. |
| `length` | optional | Exact length of a generated password. |

The built-in catalog's `from` (a value taken from another installed app) is not available to community catalogs.

### The compose template

The template is read with the variables' values and must pass these rules, then passes them again after `docker compose config` on the NAS (so the two YAML readers cannot be played against each other):

- Only `${KEY}`, `${KEY:-default}`, `${KEY-default}`, `${KEY:+x}`, `${KEY:?message}` and `$KEY` of **declared** variables; `$$` for a literal `$`. Nested variables and variables in keys are refused.
- Top-level keys: `services` (1 to 20), `volumes`, `networks`, `version` and `x-*` extensions. No `include`, no `name`, no `secrets` or `configs`.
- Each service needs an `image` (nothing is built on the NAS) and uses only these keys: `image container_name restart ports expose volumes environment command entrypoint depends_on healthcheck labels networks network_mode privileged devices cap_add cap_drop security_opt user working_dir hostname domainname tmpfs shm_size ulimits stop_grace_period stop_signal init tty stdin_open read_only extra_hosts dns dns_search dns_opt sysctls mem_limit memswap_limit mem_reservation cpus cpu_shares pids_limit group_add pid ipc userns_mode uts cgroup platform pull_policy mac_address oom_score_adj deploy logging` and `x-*`. Refused among others: `build`, `env_file`, `extends`, `volumes_from`, `runtime`, `cgroup_parent`, `secrets`, `configs`.
- `network_mode`: `bridge`, `none`, `host` (needs approval) or `service:<another service>`; `pid`, `ipc`, `uts`, `userns_mode`, `cgroup`: `host` needs approval, `container:` is refused.
- Volumes: named volumes declared at the top level, without `name`, `external`, `driver_opts` (a bind mount in disguise) or a driver other than `local`; bind mounts by absolute path or `./…` inside the app's own folder (`..` leaving it and `~` are refused); tmpfs. Mount propagation `shared`/`rshared` is refused.
- Networks declared at the top level: the `bridge` driver only, without `name` or `external` (no joining another app's network).
- `devices`: paths under `/dev` only. `logging`: `json-file` or `local`. `deploy`: `resources` (without device reservations) and `restart_policy`.

### What needs an approval

The install dialog lists everything the app gets: images, ports of the NAS, folders, Docker volumes. These need a ticked box each, and the API refuses an install or an update whose approval does not cover them exactly:

- **privileged mode** (shown in red: full control of the NAS);
- **bind mounts outside `/srv/strata`** and the app's own folder (each path; the Docker socket is shown in red, it controls every container). A path under `/srv/strata` that is a symbolic link to elsewhere counts as its target;
- the **host network**, **host namespaces** (`pid`, `ipc`, `uts`, `userns_mode`, `cgroup`: `host`);
- each **device**, each added **capability**, each **security option**.

An update shows the new version's list and marks what the previous approval did not include.

Keep apps in `/srv/strata` (variables of type `path` with defaults like `/srv/strata/apps/<id>`), pin image tags (`:v1.10.4` rather than `:latest`, so that a catalog change is what updates an app), and avoid anything that needs an approval unless the app really requires it.

## Installed apps

A community app is installed as a Compose stack (`/var/lib/strata/stacks/<name>`), with `.strata-community.json` next to it (catalog, app id, version, what was approved). Removing a catalog keeps its installed apps (they no longer get updates); they are uninstalled like any app.
