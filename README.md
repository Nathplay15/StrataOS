# StrataOS

**StrataOS** transforme une installation Debian 13 (trixie) en un véritable OS de NAS, dans l'esprit de fnOS / fygoOS, Synology DSM ou TrueNAS. Tout se pilote depuis une interface web moderne : `http://<ip-du-nas>:7070` (HTTPS sur le port 7443).

> *Strata* = les couches : disques → RAID / pools → volumes → partages → applications.

## Interface

Disponible en français et en anglais (choix dans le menu utilisateur ou sur l'écran de connexion ; par défaut, la langue du navigateur). Les textes sont écrits en français dans le code, entourés de `tr("…")` ; `frontend/src/i18n/en.json` contient leur traduction (`node frontend/scripts/i18n-extract.cjs` liste les textes et signale ceux qui manquent) ; les textes envoyés par le serveur (tâches, historique, alertes) sont traduits à l'affichage par les motifs de `frontend/src/i18n/server.ts`.

Sur ordinateur, StrataOS s'affiche comme un **bureau** à la manière de Synology DSM ou fnOS : icônes d'applications, fenêtres déplaçables et redimensionnables (plusieurs à la fois, aimantation sur les bords, agrandir/réduire), barre des tâches, menu principal avec recherche, widgets (état, ressources, stockage, applications), fonds d'écran (clic droit sur le bureau), raccourcis vers les applications installées depuis l'App Center. Les pages sont regroupées en suites : **Gestionnaire de stockage**, **Panneau de configuration**, **Docker**.

Sur mobile et petits écrans, l'interface classique (menu latéral) est utilisée. On bascule de l'une à l'autre depuis le menu utilisateur (« Interface classique » / « Mode bureau »).

## Fonctionnalités

| Domaine | Ce qui est géré |
|---|---|
| **Disques** | Inventaire (HDD, SSD, NVMe, USB), état SMART détaillé, températures, tests SMART court/long, effacement sécurisé |
| **RAID mdadm** | RAID 0, 1, 4, 5, 6, 10 et JBOD, disques de secours, ajout/retrait/panne d'un disque, agrandissement, vérification (scrub), suivi de la reconstruction, `mdadm.conf` + initramfs |
| **Btrfs** | Volumes multi-disques (single, RAID 0/1/1C3/1C4/10/5/6), compression zstd, sous-volumes, snapshots en lecture seule, scrub, ajout/retrait de disques, conversion de profil RAID à chaud |
| **ZFS** | Installation en un clic (DKMS), pools miroir / RAIDZ1-2-3 / stripe multi-vdev, cache L2ARC, SLOG, secours, chiffrement natif, datasets (compression, quotas), snapshots / restauration, scrub, TRIM, import/export |
| **LVM** | Groupes de volumes, volumes logiques (linéaire, striped, RAID LVM), extension à chaud (+ système de fichiers), snapshots |
| **Volumes** | ext4, XFS, Btrfs sur disque, grappe RAID ou LV ; montage dans `/srv/strata/<nom>` via `/etc/fstab` (UUID, `nofail`) ; import de disques existants |
| **Partages** | SMB (macOS fruit, Time Machine, corbeille réseau, invité, masqué) et NFS (clients, options), permissions par utilisateur et par groupe (Samba + ACL POSIX), sessions SMB actives |
| **Utilisateurs** | Comptes Linux + Samba synchronisés, rôles admin/utilisateur, groupes, accès SSH et Docker, désactivation, changement de mot de passe |
| **Docker** | Conteneurs (création, start/stop/restart/pause, logs en direct, console web, statistiques CPU/RAM, inspection), images (pull avec progression), volumes, réseaux, nettoyage |
| **Compose** | Stacks docker-compose avec éditeur `compose.yaml` + `.env`, déploiement, mise à jour des images, journaux |
| **App Center** | 33 applications prêtes : Jellyfin, Plex, Nextcloud, Immich, PhotoPrism, Home Assistant, Vaultwarden, qBittorrent, Transmission, Sonarr, Radarr, Prowlarr, Jellyseerr, Syncthing, AdGuard Home, Pi-hole, Nginx Proxy Manager, WireGuard (wg-easy), Uptime Kuma, Homepage, Grafana, Gitea, code-server, Navidrome, Audiobookshelf, Calibre-Web, File Browser, Paperless-ngx, Stirling-PDF, Mealie, MinIO, Duplicati, Portainer ; mises à jour automatiques planifiables (nouvelles images, conteneurs recréés seulement si besoin, anciennes images supprimées) |
| **Fichiers** | Explorateur web : navigation, envoi par glisser-déposer, téléchargement (dossiers en ZIP), aperçu image/vidéo/PDF/texte, renommer, supprimer — limité aux partages autorisés pour les non-admins |
| **Système** | Tableau de bord temps réel, moniteur (CPU par cœur, RAM, réseau, E/S disques, capteurs), services, réseau, nom d'hôte, fuseau horaire, mises à jour apt, journaux systemd, journal d'audit, terminal root web, redémarrage/arrêt |
| **Tâches planifiées** | Mises à jour des applications Docker, snapshots automatiques ZFS (récursifs) et Btrfs (volume ou sous-volume) avec rétention par nombre et/ou par âge, scrubs RAID mdadm / ZFS / Btrfs, tests SMART courts et longs ; planification quotidienne, hebdomadaire, mensuelle ou cron, aperçu des prochaines exécutions, historique, exécution manuelle, plan de maintenance recommandé en un clic |
| **Sauvegarde** | Copie rsync (volume, disque USB, serveur SSH), sauvegarde versionnée chiffrée restic (dossier local, SFTP, S3/B2/MinIO, serveur REST) avec rétention et restauration depuis l'interface, réplication ZFS incrémentale (pool local ou distant via SSH), planification, clé SSH propre à StrataOS |
| **Réseau** | Adresse fixe / DHCP / sans IP par interface, agrégation de liens (actif/secours, LACP 802.3ad, équilibrage…), ponts (bridges) pour les machines virtuelles, VLAN, MTU, DNS ; chaque changement est appliqué **à l'essai** et annulé automatiquement s'il n'est pas confirmé (même après un redémarrage du service) |
| **Comptes** | Quotas par utilisateur (ext4, XFS, ZFS), double authentification TOTP avec codes de secours et réinitialisation par l'administrateur, certificat HTTPS importé ou Let's Encrypt automatique |
| **Machines virtuelles** | KVM/libvirt installé en un clic : création guidée (Linux, Windows 10/11 avec TPM 2.0 et UEFI, autre), disques qcow2 dans un dossier partagé, ISO choisie parmi les fichiers des volumes, réseau NAT ou pont sur le réseau local, console graphique dans le navigateur (noVNC), démarrage/arrêt/pause/arrêt forcé, démarrage avec le NAS, changement de CD et de ressources |
| **Synchronisation cloud** | rclone : Google Drive, OneDrive, Dropbox, S3 (AWS, Wasabi, MinIO, R2…), Backblaze B2, WebDAV/Nextcloud, SFTP ; envoi, téléchargement (avec ou sans miroir) ou bidirectionnelle, exclusions, limite de bande passante, planification, test de connexion et choix du dossier distant, jetons OAuth rafraîchis conservés, identifiants jamais renvoyés au navigateur |
| **Domaine / LDAP** | Jonction à Active Directory (Samba serveur membre, winbind, Kerberos ; mot de passe administrateur jamais enregistré), droits des partages pour les utilisateurs et groupes du domaine (`DOMAINE\utilisateur`), connexion à l'interface web et à WebDAV avec les comptes de l'annuaire (groupe d'administrateurs configurable), sortie propre du domaine ; annuaire LDAP via sssd (identités POSIX pour les ACL et NFS) |
| **Services de fichiers** | WebDAV intégré (HTTPS, droits des partages, corbeille), FTP/FTPS (vsftpd, chiffrement exigible, ports passifs), SFTP confiné au stockage pour les utilisateurs sans accès SSH ; tout passe par les mêmes droits que SMB |
| **Antivirus** | ClamAV installé en un clic, signatures mises à jour automatiquement, analyses planifiées (un partage ou tout le stockage) en priorité basse, quarantaine avec restauration, alerte « virus détecté » |
| **Onduleur** | NUT : onduleur USB/série/SNMP avec détection, client d'un autre serveur NUT (Synology…), partage de l'onduleur sur le réseau, arrêt automatique (batterie faible ou après X minutes), alertes, mode simulation pour tester |
| **Mises à jour** | Dépôt APT signé (`make repo`), mise à jour de StrataOS en un clic depuis l'interface, exécutée hors du service pour survivre à son redémarrage |
| **Alertes** | RAID dégradé, pool ZFS en erreur, disque SMART défaillant ou qui faiblit (secteurs réalloués, usure SSD…), virus détecté, volume plein à 90 %, tâche échouée → webhook (Discord, Slack, ntfy, Gotify…) e-mail SMTP, bot Telegram et/ou notifications push ntfy |
| **Résilience** | Récupération automatique au démarrage : grappe RAID démarrée en mode dégradé s'il manque un disque, volumes remontés, NFS relancé, alertes ; disques identifiés par UUID / by-id (jamais par `/dev/sdX`, qui change d'un démarrage à l'autre) ; remplacement de disque guidé (RAID et ZFS) |
| **Sécurité** | Sessions HttpOnly/SameSite persistantes (jetons hachés), protection CSRF, gestionnaire de fichiers confiné par `os.Root` (aucune sortie via `..` ou liens symboliques), contenus utilisateurs jamais exécutés dans l'origine de l'interface (CSP sandbox), ACL disque alignées sur les droits des partages (SMB, NFS et SSH), bcrypt, limitation des tentatives de connexion, HTTPS auto-signé, audit de toutes les actions |

## Architecture

```
StrataOS/
├── backend/            Daemon Go (un seul binaire, l'UI est embarquée)
│   ├── cmd/strata/     Point d'entrée (HTTP :7070, HTTPS :7443)
│   ├── internal/
│   │   ├── api/        Routes REST + WebSocket (auth, stockage, partages, docker, fichiers, système)
│   │   ├── storage/    Disques, SMART, mdadm, LVM, Btrfs, ZFS, fstab
│   │   ├── shares/     Génération smb.conf / exports NFS, ACL
│   │   ├── accounts/   Utilisateurs et groupes Linux/Samba
│   │   ├── docker/     Client Docker Engine (socket Unix), Compose, App Center
│   │   ├── system/     Monitoring, services, journaux, réseau, mises à jour
│   │   ├── jobs/       Tâches de fond (formatage, pull, apt…)
│   │   ├── scheduler/  Planificateur cron : snapshots + rétention, scrubs, SMART
│   │   └── store/      État persistant (/var/lib/strata/state.json)
│   └── web/            go:embed du frontend compilé
├── frontend/           React 19 + TypeScript + Vite + Tailwind CSS 4 (src/desktop/ : bureau et fenêtres)
├── packaging/          Paquet .deb (systemd, postinst, scripts ZFS/Docker, Avahi)
└── vm/                 VM de test QEMU/KVM (Debian 13 + 6 disques SATA virtuels)
```

Le daemon tourne en root (il pilote mdadm, LVM, ZFS, Samba…). Il n'invente rien : il orchestre les outils standard de Debian, et la configuration reste lisible (`/etc/fstab`, `/etc/samba/smb.conf`, `/etc/exports.d/strata.exports`, `/etc/mdadm/mdadm.conf`).

## Construire

Prérequis : Go ≥ 1.24, Node ≥ 20, `dpkg-deb`.

```bash
make deb          # frontend + backend + dist/strataos_<version>_amd64.deb
make backend      # binaire seul : build/strata-linux-amd64
make vet          # go vet + typecheck TypeScript
make test         # tests unitaires Go (cron, rétention, corbeille, validation ZFS…)
make vm-test      # test complet sur une VM neuve (~35 min), voir vm/README.md
make repo REPO_URL=https://apt.exemple.fr   # paquet + dépôt APT signé dans dist/apt
make iso          # ISO d'installation : dist/strataos-<version>-amd64.iso (Docker requis)
```

## Installer avec l'ISO

`make iso` part de l'image netinst officielle de Debian 13 (`vm/images/`) et y ajoute une entrée **Installer StrataOS**, choisie automatiquement après 10 s (BIOS) ou 5 s (UEFI). L'installation est automatique en français : réseau DHCP, nom `strataos`, Debian minimal + SSH, puis le paquet StrataOS. Seule question posée quand la machine a plusieurs disques : celui qui recevra le système (**il est entièrement effacé**, les autres disques ne sont pas touchés).

Au redémarrage, l'écran de la machine affiche l'adresse de l'interface web, où l'assistant crée le compte administrateur (qui reçoit aussi `sudo` pour SSH). Le compte root est verrouillé.

Gravez l'ISO sur une clé USB avec Balena Etcher, Rufus (mode DD) ou `dd`.

## Installer sur un Debian 13 existant

```bash
sudo apt install ./strataos_0.2.0_amd64.deb
```

Les dépendances (mdadm, lvm2, btrfs-progs, xfsprogs, smartmontools, samba, nfs-kernel-server, docker.io, docker-compose, avahi, wsdd2…) sont installées automatiquement. Ouvrez ensuite `http://<ip>:7070` : l'assistant crée le compte administrateur. ZFS s'installe depuis **Stockage → ZFS** (section contrib de Debian, module DKMS).

## Développer avec la VM de test

```bash
make vm-create    # télécharge Debian 13, crée la VM + 6 disques de 10 Go, premier boot
make vm-deploy    # construit le .deb et l'installe dans la VM
make dev          # UI en hot-reload sur http://localhost:5173 (API → VM)
make vm-ssh       # shell dans la VM
make vm-stop
```

Voir [vm/README.md](vm/README.md) pour les détails.

## Feuille de route

- Réplication et bascule entre deux NAS StrataOS (haute disponibilité)
- Application mobile de fichiers
