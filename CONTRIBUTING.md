# Contributing to StrataOS

Thanks for your interest in StrataOS! Bug reports, hardware reports, translations and code are all welcome.

## Reporting a problem

- **Bugs**: open an issue with the **Bug report** form. Include the StrataOS version (**Updates** page), how it was installed (ISO, Raspberry Pi image or package), what you did, and what happened. Logs help a lot: `journalctl -u strata -n 200` on the NAS.
- **Hardware**: tried StrataOS on real hardware? The **Hardware report** form tells others what works, even when everything does.
- **Security issues**: please don't open a public issue. See [SECURITY.md](SECURITY.md).
- **Ideas**: the **Feature request** form. Explain the problem you want solved, not only the solution.

## Translations

The interface is written in English. Each language has a dictionary in `frontend/src/i18n/<lang>.json` (English text → translation), and the messages written by the server are in `frontend/src/i18n/server.<lang>.json`. Fixing a clumsy translation is a great first contribution: edit the value, keep `{0}`, `{1}`… placeholders as they are, and open a pull request.

To add a language, copy `fr.json`, translate the values, and add the language to `frontend/src/lib/i18n.ts` and `backend/internal/i18n/i18n.go`.

## Code

Everything you need is in [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md): layout, build, test VM and tests.

Before opening a pull request:

1. `make vet` and `make test` pass, and `node frontend/scripts/i18n-extract.cjs --check` reports nothing missing (new interface texts are wrapped in `tr("…")` and translated in every language).
2. Changes touching storage, shares, users or the system are checked on the test VM (`make vm-deploy`); big changes ideally with `make vm-test`.
3. The code reads like the code around it: same naming, comment density and style. Go is `gofmt`-ed.
4. One topic per pull request, with a description of what changes and how you tested it.

StrataOS runs as root and manages people's data: be conservative. Never pass user input to a shell, validate every name and path, and prefer the standard Debian tools over clever tricks.

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
