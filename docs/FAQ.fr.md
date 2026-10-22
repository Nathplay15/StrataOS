# Questions fréquentes

Des réponses courtes aux questions qui reviennent le plus. Le [guide d'utilisation](GUIDE.fr.md) donne le détail, étape par étape.

## Installation

### De quel matériel ai-je besoin ?

Un processeur 64 bits (x86-64 ou ARMv8) à 2 cœurs, 2 Go de mémoire, un disque système de 16 Go et au moins un disque de données. 8 Go de mémoire et un SSD séparé pour le système sont un bon départ ; ZFS, les machines virtuelles et de nombreuses applications Docker en demandent plus. La [page matériel](HARDWARE.md) liste ce qui a été testé.

### Quel fichier télécharger ?

- `strataos-<version>-amd64.iso` pour un PC ou un serveur (BIOS et UEFI) ;
- `strataos-<version>-arm64.iso` pour une carte ou un serveur ARM 64 bits qui démarre en UEFI ;
- `strataos-<version>-raspberrypi.img.xz` pour un Raspberry Pi 5, 500, CM5, 4, 400, CM4 ou 3 ;
- `strataos_<version>_<arch>.deb` pour ajouter StrataOS à un système Debian 13 existant.

### L'installation efface-t-elle mes disques ?

Seulement le disque système. Quand la machine a plusieurs disques, l'installation demande lequel reçoit le système ; ce disque est effacé et les autres ne sont pas touchés. Des disques qui contiennent déjà des données peuvent être repris ensuite avec Stockage → Volumes → **Importer un système de fichiers existant** (ou en important un pool ZFS), sans les formater.

### Puis-je installer StrataOS sur Debian 12, Ubuntu ou une autre distribution ?

Non. StrataOS est conçu pour Debian 13 (trixie) et ses paquets. Sur un système Debian 13 existant, installez le paquet `.deb` avec `sudo apt install ./strataos_<version>_<arch>.deb`.

### Où est l'interface web ?

À l'adresse `http://<adresse>:7070`, ou `https://<adresse>:7443` en HTTPS. L'écran de la machine affiche l'adresse au démarrage. En HTTPS, le certificat est auto-signé tant que vous n'importez pas le vôtre ou n'en obtenez pas un de Let's Encrypt : le navigateur vous avertit la première fois.

### J'ai installé StrataOS mais je ne le trouve pas sur le réseau.

Vérifiez le câble réseau et regardez l'adresse sur l'écran de la machine. Sans écran, consultez la liste des appareils de votre box. Sur un Raspberry Pi, le premier démarrage prend deux à trois minutes, le temps que le système s'étende à toute la carte. Depuis une console, `systemctl status strata` indique si le service tourne.

## Disques et RAID

### Quel type de stockage choisir ?

| Vous avez | Un bon choix |
|---|---|
| 1 disque | Un volume simple (ext4, XFS ou Btrfs). Aucune protection contre une panne de disque : sauvegardez-le. |
| 2 disques | RAID 1 (mdadm) ou un volume Btrfs RAID 1 : tout est écrit deux fois. |
| 3 disques ou plus | RAID 5 (un disque peut tomber en panne), ou un pool ZFS RAIDZ. |
| 4 disques ou plus | RAID 6 ou RAIDZ2 (deux disques peuvent tomber en panne), ou Btrfs RAID 10. |
| Beaucoup de mémoire et l'envie de sommes de contrôle partout | ZFS (Stockage → ZFS, installé en un clic). |

Btrfs et ZFS ajoutent les snapshots et des sommes de contrôle qui détectent les données abîmées. Btrfs tire aussi bien parti de disques de tailles différentes. Le RAID mdadm utilise la taille du plus petit disque sur chaque disque.

### Le RAID est-il une sauvegarde ?

Non. Le RAID permet au NAS de continuer à fonctionner quand un disque tombe en panne. Il ne protège ni d'un fichier supprimé, ni d'un rançongiciel, ni d'un incendie, ni d'un vol. Pour cela, il faut des snapshots et des sauvegardes (voir plus bas).

### Puis-je ajouter un disque plus tard ?

Oui. Une grappe mdadm s'agrandit : ajoutez le nouveau disque comme disque de secours, puis **Agrandir** la grappe (la restructuration peut prendre des heures) et agrandissez le volume. Un pool ZFS s'agrandit en ajoutant un autre vdev, et les groupes de volumes LVM peuvent être étendus. Prévoyez l'organisation dès le départ si vous le pouvez : c'est plus simple.

### Comment remplacer un disque en panne ?

Stockage → RAID (ou ZFS) → **Remplacer**, puis choisissez le nouveau disque. La reconstruction démarre d'elle-même, et vous recevez une notification à son début et une à sa fin. Lancez un **test de burn-in** sur les disques neufs avant de leur confier des données.

### Puis-je utiliser des disques USB ?

Pour les sauvegardes, oui. Pour une grappe RAID, évitez-les si possible : certains boîtiers USB cachent les données SMART ou se déconnectent sous la charge. Sur un Raspberry Pi, préférez des disques avec leur propre alimentation ou un hub alimenté.

## Sauvegardes

### Qu'est-ce que la règle 3-2-1 ?

Gardez **3** copies de vos données, sur **2** types de supports différents, dont **1** hors site. Avec StrataOS, par exemple : les fichiers sur le NAS, des snapshots sur le NAS pour annuler rapidement, une sauvegarde versionnée sur un disque USB, et une autre dans le cloud ou sur un second NAS chez un proche.

### Quel type de sauvegarde choisir ?

- **Sauvegarde versionnée** (restic) : chiffrée, dédupliquée, avec historique. Le meilleur choix par défaut, vers un dossier, un disque USB, un serveur SFTP, un stockage S3 ou un coffre sur un autre StrataOS.
- **Copie de fichiers** (rsync) : un miroir simple, lisible partout, mais sans historique.
- **Réplication ZFS** : pour les datasets ZFS, vers un autre pool ou un autre hôte ZFS. Le mode « récupérer » met les sauvegardes hors de portée de la source.
- **Sauvegarder un autre NAS** : ce NAS récupère les dossiers d'un autre NAS (rsync, SSH ou SMB).

La synchronisation bidirectionnelle et la synchronisation cloud gardent des dossiers identiques, mais propagent aussi les suppressions : elles ne remplacent pas une sauvegarde.

### Que se passe-t-il si je perds le mot de passe restic ?

La sauvegarde ne peut pas être restaurée : personne, StrataOS compris, ne peut la déchiffrer. Gardez le mot de passe en lieu sûr, hors du NAS. Testez une restauration de temps en temps avec le bouton **Restaurer**.

### Les sauvegardes contiennent-elles mes réglages ?

Pas par défaut. Paramètres → **Sauvegarde de la configuration** exporte les réglages, comptes, dossiers partagés, tâches et applications (pas vos fichiers), et peut en faire une copie chaque jour. Choisissez un dossier partagé qui est lui-même sauvegardé.

## Mises à jour et retour en arrière

### Comment mettre à jour StrataOS ?

Mises à jour → **Mettre à jour StrataOS**. StrataOS se met à jour depuis son dépôt de paquets signé ; les mises à jour de Debian sont listées sur la même page. Les systèmes installés en version 0.4 ou antérieure doivent installer une fois à la main le paquet 0.5.

### Et si une mise à jour se passe mal ?

Un **point de restauration** est créé avant chaque mise à jour. Mises à jour → Restaurer ramène le système et ses programmes à ce point, avec un redémarrage. Vos données et vos réglages StrataOS ne sont jamais touchés. Sur un système installé depuis l'ISO StrataOS, les points de restauration sont des snapshots Btrfs du système ; sur un système LVM, des snapshots LVM ; ailleurs (l'image Raspberry Pi, par exemple), StrataOS garde les versions précédentes des paquets qu'il met à jour.

### Les applications sont-elles aussi mises à jour ?

Les applications de l'App Center se mettent à jour séparément, à la main ou selon un planning (Tâches planifiées).

## Accès à distance

### Comment joindre mon NAS depuis l'extérieur ?

Du plus sûr au plus exposé :

1. **VPN WireGuard** (Accès à distance) : un port UDP redirigé sur votre box, puis chaque appareil se connecte comme s'il était à la maison. Activez le DNS dynamique si votre adresse publique change.
2. **Tailscale** : aucun port à ouvrir ; connectez-vous avec le même compte sur le NAS et sur vos appareils.
3. **Cloudflare Tunnel** : publie les services web choisis sous votre domaine, sans ouvrir de port.
4. **Domaines des applications** : ports 80 et 443 redirigés, chaque application sous son propre nom avec un certificat Let's Encrypt.

### Dois-je ouvrir le port 7070 sur ma box ?

Évitez-le. Utilisez plutôt le VPN ou Tailscale. Si vous devez publier l'interface web, activez le HTTPS, la double authentification et le blocage d'adresses.

## Raspberry Pi

### Quels modèles de Raspberry Pi fonctionnent ?

Les Raspberry Pi 5, 500, CM5, 4, 400 et CM4 gèrent le partage de fichiers, les sauvegardes et les applications Docker ; 4 Go de mémoire ou plus sont recommandés. Les Raspberry Pi 3 B et 3 B+ conviennent au partage de fichiers simple et aux sauvegardes, mais avec 1 Go de mémoire et l'USB 2.0, ils ne sont pas adaptés aux applications Docker, à ZFS ni aux machines virtuelles.

### Qu'est-ce qui change sur un Raspberry Pi ?

L'image est une Debian 13 avec le noyau et le firmware Raspberry Pi. Docker n'est pas préinstallé (App Center → **Installer Docker**), et la cible iSCSI n'est pas disponible car le noyau Raspberry Pi ne l'inclut pas. La [page matériel](HARDWARE.md) donne des conseils sur l'alimentation, les SSD NVMe du Pi 5 et les pages mémoire de 16 Ko.

## Applications Docker

### Faut-il connaître Docker ?

Non. L'App Center installe des applications prêtes à l'emploi (Jellyfin, Nextcloud, Immich, Home Assistant…) en un clic, avec un formulaire pour leurs dossiers et leurs ports. **Mes apps** les liste, avec un bouton Ouvrir.

### Puis-je lancer mes propres conteneurs ?

Oui : **Ajouter une app personnalisée** à partir de votre propre fichier compose, ou ajoutez une pile Compose existante à Mes apps. N'utilisez que des fichiers compose de confiance : les conteneurs ont les droits que vous leur donnez.

### Où les applications gardent-elles leurs données ?

Dans les dossiers indiqués dans le formulaire d'installation (par exemple `/srv/strata/apps/<application>` pour les réglages, et vos dossiers de médias pour le contenu). Gardez-les sur un volume sauvegardé.

## Sécurité

### Comment protéger le NAS ?

- Un compte par personne, des administrateurs avec la double authentification ou une clé d'accès.
- Les notifications activées, pour être prévenu d'un disque défaillant ou d'une attaque bloquée.
- Des snapshots sur Btrfs ou ZFS, verrouillés pour les mettre à l'abri des rançongiciels, et des sauvegardes hors du NAS.
- La protection contre les rançongiciels avec les fichiers pièges, l'antivirus et le blocage d'adresses (pages Antivirus et Blocage d'adresses).
- Des mises à jour installées régulièrement.

### StrataOS envoie-t-il des données quelque part ?

StrataOS n'a pas de télémétrie. Il contacte son dépôt de mises à jour et les miroirs Debian, les registres Docker des applications que vous installez, et les services que vous configurez (notifications, stockage cloud, accès à distance, Let's Encrypt). L'accès à distance consulte votre adresse publique auprès d'un service public, et votre navigateur charge les icônes de l'App Center depuis un CDN public.

### J'ai trouvé une faille de sécurité.

N'ouvrez pas de ticket public : signalez-la en privé (voir [Contribuer et sécurité](../SECURITY.md)).

## Signaler un bug

### Comment signaler un bug ?

Ouvrez un ticket sur GitHub avec le formulaire **Bug report**. Indiquez la version de StrataOS (page Mises à jour), comment elle a été installée (ISO, image Raspberry Pi ou paquet), ce que vous avez fait et ce qui s'est passé. Les journaux du service aident beaucoup : `journalctl -u strata -n 200` sur le NAS.

### StrataOS fonctionne sur mon matériel. Dois-je vous le dire ?

Oui, s'il vous plaît : un [rapport matériel](https://github.com/Nathplay15/StrataOS/issues/new?template=hardware_report.yml) indique aux autres ce qui fonctionne, même quand tout fonctionne.
