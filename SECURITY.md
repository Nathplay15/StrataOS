# Security policy

StrataOS runs as root and holds people's files, so security reports are taken seriously.

## Reporting a vulnerability

Please **don't open a public issue**. Report it privately through GitHub: **Security → Report a vulnerability** on this repository ([direct link](https://github.com/Nathplay15/StrataOS/security/advisories/new)).

Include the affected version, the steps to reproduce, and the impact you see. You'll get an answer as soon as possible; once a fix is released, the advisory is published with credit to you if you wish.

## Supported versions

Only the latest release gets security fixes. Updates are one click away (**Updates → Update StrataOS**), and a restore point is taken before each one.

## Scope

In scope: the StrataOS daemon and web interface, the packages, installer ISOs and Raspberry Pi image, and the update repository. Vulnerabilities in Debian packages or Docker apps installed from the App Center should be reported to their own projects, unless StrataOS configures them insecurely.
