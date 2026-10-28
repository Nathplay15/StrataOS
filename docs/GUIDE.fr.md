# Guide d'utilisation de StrataOS

Ce guide explique, étape par étape, comment installer StrataOS et s'en servir au quotidien. Chaque page de l'interface rappelle aussi l'essentiel ; ce guide donne l'ordre logique et les bonnes pratiques.

## 1. Installer StrataOS

**Sur un PC ou un serveur (amd64)** : téléchargez `strataos-<version>-amd64.iso` dans les Releases, écrivez-la sur une clé USB (balenaEtcher, Rufus en mode DD, ou `dd`), démarrez la machine sur la clé et choisissez « Install StrataOS » dans le menu de démarrage. L'installation est automatique et se déroule en anglais, avec un clavier américain et le fuseau horaire UTC par défaut (l'interface web a son propre réglage de langue) ; si la machine a plusieurs disques, l'installateur demande lequel accueille le système (les autres restent intacts pour vos données). Comptez 10 à 20 minutes.

**Sur une carte ARM 64 bits (arm64)** : même chose avec `strataos-<version>-arm64.iso` sur une machine qui démarre en UEFI.

**Sur un Raspberry Pi 5, 500, CM5, 4, 400, CM4 ou 3 B / 3 B+** : écrivez `strataos-<version>-raspberrypi.img.xz` sur une carte microSD ou un SSD USB (Raspberry Pi Imager → « Utiliser une image personnalisée », ou balenaEtcher), branchez le Pi au réseau par câble et démarrez-le. Au premier démarrage, la partition système s'agrandit toute seule ; patientez deux à trois minutes. L'image est en 64 bits. Avec 1 Go de mémoire, un Pi 3 convient au partage de fichiers simple, pas aux applications Docker, à ZFS ni aux machines virtuelles. Sur un Pi 5, le système peut aussi être installé sur un SSD NVMe (HAT M.2) : écrivez-y l'image comme sur une carte. Docker n'est pas préinstallé dans l'image Raspberry Pi, pour qu'elle reste légère : Centre d'applications → **Installer Docker** l'ajoute en un clic.

**Sur un Debian 13 existant** : `sudo apt install ./strataos_<version>_<arch>.deb`.

**Vérifiez votre téléchargement** (recommandé) : chaque version est accompagnée de `SHA256SUMS` et de sa signature `SHA256SUMS.asc`, faite avec la clé de l'archive StrataOS (celle qui signe les mises à jour). Le [site web](https://nathplay15.github.io/StrataOS/#verify) donne les deux commandes à lancer et l'empreinte de clé attendue.

À la fin, l'écran de la machine affiche l'adresse de l'interface et un QR code : `http://<adresse>:7070` (HTTPS : port 7443). La section 2 présente cet écran.

## 2. Premier démarrage

Ouvrez l'adresse dans un navigateur : l'assistant demande la langue, le nom du NAS et le compte administrateur (ce compte peut aussi utiliser `sudo` en SSH). Il propose ensuite de préparer le stockage, un premier partage, des alertes et des tâches recommandées (vérification des disques, snapshots).

Conseil : activez tout de suite les **notifications** (Paramètres → Notifications : e-mail, Telegram, ntfy ou webhook) pour être prévenu d'une panne de disque. Sur une nouvelle installation, les notifications sont en anglais par défaut ; changez leur langue au même endroit (Langue des notifications). La section 19 explique comment choisir ce que reçoit chaque canal.

**L'écran du NAS** : un écran ou un téléviseur branché sur le NAS affiche ses adresses web, un QR code pour ouvrir l'interface depuis un téléphone, son état de santé (alertes) et le remplissage de ses volumes. Avec un clavier, son menu (en anglais ; flèches ou 1 à 5, puis Entrée) propose :

- **Network details** : les interfaces réseau et leurs adresses.
- **Reset the network to DHCP** : toutes les interfaces repassent en adressage automatique (les adresses fixes, agrégations, ponts et VLAN sont supprimés). Vous avez ensuite 3 minutes pour garder la nouvelle configuration ; sinon, l'ancienne revient d'elle-même.
- **Administrator recovery code** : un code à usage unique, valable 15 minutes, pour réinitialiser un mot de passe administrateur oublié (voir la section 21).
- **Restart or shut down** : redémarrer ou éteindre.
- **Login prompt (administrators)** : une connexion normale sur la deuxième console (Alt+F2) ; Alt+F1 ramène l'écran StrataOS.

Paramètres → **Écran console** désactive cet écran (« Écran StrataOS sur l'affichage du NAS »), ou seulement les codes de récupération (« Codes de récupération administrateur depuis l'écran »).

## 3. Le stockage

Le stockage s'organise en couches : **disques → grappe RAID ou pool → volume → dossiers partagés**.

1. **Disques** : vérifiez l'état SMART de chaque disque. Un disque « qui faiblit » doit être remplacé bientôt.
2. **Redondance** : choisissez une protection contre la panne d'un disque :
   - grappe **RAID mdadm** (RAID 1 avec 2 disques, RAID 5 avec 3 ou plus, RAID 6 avec 4 ou plus), puis un volume ext4 ou XFS dessus ;
   - ou un volume **Btrfs** sur plusieurs disques (profil RAID 1 ou 10) ;
   - ou un **pool ZFS** (Stockage → ZFS, installation en un clic), miroir ou RAIDZ.
3. **Volume** : il est monté dans `/srv/strata/<nom>`. Un volume sur un seul périphérique peut recevoir un **cache SSD** dès sa création.

Le RAID ne remplace pas une sauvegarde : il protège d'une panne de disque, pas d'une suppression, d'un rançongiciel ou d'un incendie.

**Testez les nouveaux disques** : avant d'y mettre des données, lancez un **Test de burn-in** (Stockage → Disques, sur un disque libre). Il enchaîne un test SMART court, un test de surface de chaque bloc et un test SMART étendu, puis compare les compteurs de secteurs avant et après. Choisissez « Lecture seule (non destructif) », ou « Test d'écriture (efface le disque) » pour la vérification la plus poussée. Un test complet d'un gros disque dure un jour ou plus ; le résultat vous est notifié.

Pour remplacer un disque en panne : Stockage → RAID (ou ZFS), « Remplacer », choisissez le nouveau disque ; la reconstruction démarre seule. Vous recevez une notification au début de la reconstruction et une à la fin, pas une à chaque étape.

**iSCSI** (Stockage → iSCSI) fournit des disques réseau à des serveurs VMware, Proxmox, Hyper-V, Windows ou Linux. « Créer une cible », ajoutez un ou plusieurs **LUN** (fichier image sur un volume, volume logique LVM ou volume ZFS), puis indiquez les **initiateurs autorisés** : personne ne peut se connecter tant que la liste est vide. Activez l'**authentification CHAP** si le réseau de stockage n'est pas isolé, et ne laissez jamais deux ordinateurs écrire sur le même LUN sans système de fichiers en cluster.

## 4. Libérer de l'espace

- **Analyseur d'espace** (Stockage → Analyseur d'espace) : montre ce qui occupe de la place dans un volume ou un dossier partagé, par dossier et par type de fichier, avec les plus gros fichiers et les gros fichiers non modifiés depuis un an. L'analyse tourne en arrière-plan ; son résultat est conservé jusqu'à la suivante.
- **Doublons** (Stockage → Doublons) : trouve les fichiers identiques (même contenu, pas seulement même nom). Cochez les copies à supprimer, ou utilisez « Tout sélectionner sauf les plus anciens ». Au moins une copie de chaque groupe est toujours conservée, et les fichiers partent dans la corbeille de leur dossier partagé quand il en a une.

## 5. Utilisateurs et dossiers partagés

1. **Utilisateurs** : créez un compte par personne, regroupez-les en **groupes** (ex. « famille », « compta »). Activez la double authentification pour les administrateurs.
2. **Dossiers partagés** : créez un partage par usage (Documents, Photos, Médias…), sur le volume de votre choix, puis donnez les droits : lecture et écriture, lecture seule ou aucun accès, par utilisateur ou par groupe.
3. Options utiles : **corbeille réseau** (les fichiers supprimés sont gardés dans `.recycle`), **quota** du partage (passage en lecture seule une fois plein si vous le souhaitez), **chiffrement** (voir 12).

## 6. Accéder aux fichiers

- **Windows** : dans l'explorateur, tapez `\\<adresse>\<partage>` ; connectez-vous avec votre compte StrataOS.
- **macOS** : Finder → Aller → Se connecter au serveur → `smb://<adresse>/<partage>`. Pour les sauvegardes Time Machine, voir 9.
- **Linux** : `smb://` dans le gestionnaire de fichiers, ou NFS si le partage l'autorise.
- **Navigateur** : l'application **Fichiers** permet d'envoyer (des fichiers ou tout un dossier), télécharger (dossiers en ZIP), renommer et créer un « Nouveau fichier ». Les fichiers s'ouvrent dans des onglets, pour en garder plusieurs ouverts côte à côte : texte et code dans un éditeur (coloration syntaxique, rechercher-remplacer, Ctrl+S ; jusqu'à 5 Mo, en lecture seule dans les partages en lecture seule), Markdown avec aperçu en direct, images, PDF, audio et vidéo. Si quelqu'un a modifié le fichier pendant que vous l'éditiez, StrataOS demande s'il faut le recharger ou l'écraser.
- **Afficher les fichiers** : double-cliquez sur un fichier pour l'ouvrir dans un onglet. Les PDF s'ouvrent dans la visionneuse intégrée (recherche, zoom, impression) ; les fichiers Word et Excel (et CSV) s'affichent directement ; toutes les vidéos et tous les sons se lisent, même dans des formats que le navigateur ne connaît pas (le NAS les convertit à la volée) ; les photos HEIC, TIFF et RAW s'affichent ; les archives zip, tar, 7z et RAR se parcourent et s'extraient. PowerPoint et les autres documents demandent le composant facultatif **Aperçus de documents** (Paramètres → Composants, environ 400 Mo). « Ouvrir avec » permet de choisir entre la visionneuse, l'éditeur de texte, OnlyOffice et le téléchargement ; le panneau de détails indique le type, le propriétaire et un SHA-256 à la demande.
- **Documents bureautiques** : installez **OnlyOffice Docs** depuis l'App Center (environ 3 Go), puis activez « Ouvrir les documents bureautiques avec OnlyOffice » dans Paramètres (carte Documents bureautiques). Les fichiers Word, Excel et PowerPoint (et OpenDocument) s'ouvrent alors dans OnlyOffice ; plusieurs personnes peuvent éditer le même document ensemble, et celles qui n'ont que la lecture sur le partage l'ouvrent en lecture seule. Le conteneur joint StrataOS à l'adresse `http://host.docker.internal:7070` (le port HTTP doit rester actif) ; un autre serveur de documents peut être indiqué au même endroit. Si vous utilisez StrataOS en HTTPS, donnez aussi une adresse HTTPS au serveur de documents (les navigateurs bloquent un éditeur HTTP dans une page HTTPS).
- **Téléphones Android** : l'application StrataOS parcourt vos fichiers et sauvegarde vos photos (voir la section 22).
- **Ordinateurs (Windows, macOS, Linux)** : l'application StrataOS Sync garde des dossiers de l'ordinateur et du NAS identiques, dans les deux sens (voir la section 23).
- **Autres appareils** : activez WebDAV, SFTP ou FTP dans Services de fichiers.

Pour **partager avec quelqu'un sans compte**, utilisez « Partager un lien » sur un fichier ou un dossier (mot de passe, expiration, nombre de téléchargements, ou boîte de dépôt pour recevoir des fichiers). Retrouvez et supprimez vos liens dans **Liens partagés**.

## 7. Photos

Un administrateur choisit les dossiers de photos (Photos → Dossiers). La chronologie affiche photos et vidéos par mois ; chacun ne voit que les partages auxquels il a accès. Sélectionnez des photos pour créer des **albums**, partageables par lien.

## 8. Médias : TV et enceintes

La page **Médias** amène vos vidéos, votre musique et vos photos sur les écrans et les enceintes de la maison.

- **Serveur DLNA** : les TV, consoles de jeu et lecteurs multimédias du réseau local trouvent le NAS d'eux-mêmes et parcourent les « Dossiers publiés ». DLNA n'a pas de mot de passe : tous les appareils de la maison voient ces dossiers, même si le dossier partagé est privé ; ne publiez que ce que tout le monde peut regarder.
- **Musique et enceintes** : installez **OwnTone** depuis l'App Center, choisissez vos dossiers de musique, puis jouez votre bibliothèque sur des enceintes **AirPlay** et **Chromecast**. Activez plusieurs enceintes pour jouer dans toutes les pièces à la fois, en synchronisation. Par défaut, seuls les administrateurs contrôlent la musique ; vous pouvez l'ouvrir à chaque utilisateur.
- **Diffuser** : dans Fichiers ou Photos, le bouton **Diffuser** lit une vidéo, un morceau ou une image sur une ou plusieurs TV (Chromecast ou DLNA). La TV lit le fichier d'origine et doit donc prendre en charge son format ; pour convertir à la volée, utilisez Jellyfin depuis l'App Center. L'onglet Diffuser montre ce qui est en lecture et permet de le mettre en pause ou de l'arrêter.

## 9. Sauvegarder

Gardez au moins une copie de vos données ailleurs que sur le NAS (règle 3-2-1 : 3 copies, 2 supports, 1 hors site). Dans **Sauvegarde**, « Créer une sauvegarde » propose :

- **Copie** : un miroir rsync vers un disque USB, un volume ou un serveur SSH.
- **Sauvegarder un autre NAS** : ce NAS récupère des dossiers d'un autre NAS ou serveur, par **rsync via SSH**, un **démon rsync** (proposé par Synology, QNAP et TrueNAS) ou un **partage SMB** (Windows, tout NAS). Avec « Conserver des versions (snapshots) », un snapshot en lecture seule de la destination est pris après chaque exécution (Btrfs ou ZFS) : les états antérieurs restent récupérables même si des fichiers ont été supprimés ou chiffrés sur l'autre NAS.
- **Sauvegarde versionnée** (restic, chiffrée et dédupliquée) vers un dossier, un serveur SFTP, du stockage S3 ou un **coffre d'un autre StrataOS** : sur l'autre NAS, créez un coffre (Sauvegarde → Coffres de sauvegarde, idéalement en « ajout seul ») et affichez un code ; sur ce NAS, choisissez la destination « Coffre d'un autre NAS StrataOS » et saisissez le code. Une tâche de sauvegarde peut aussi inclure des **machines virtuelles** (voir 15).
- **Réplication ZFS** : envoie un dataset vers un autre pool, ou en **récupère** un depuis un hôte ZFS distant (pull). En mode pull, la source n'a aucun accès à ce NAS : même compromise, elle ne peut pas supprimer ses sauvegardes.
- **Synchronisation bidirectionnelle** : garde un dossier identique sur deux NAS, avec des modifications des deux côtés. Attention : **les suppressions se propagent dans les deux sens**, un fichier supprimé sur un NAS l'est sur l'autre à la prochaine exécution. Un snapshot du dossier local est pris avant chaque exécution (Btrfs ou ZFS), ce qui permet de rattraper une erreur. Une synchronisation seule n'est pas une sauvegarde.
- **Synchronisation cloud** (Google Drive, OneDrive, Dropbox, S3…), sur sa propre page.

**Time Machine** : la page Sauvegarde liste aussi les destinations Time Machine et les Mac qui s'y sauvegardent. « Créer une destination Time Machine » crée le dossier et le partage d'un coup, avec une limite de taille facultative ; sur le Mac, allez dans Réglages Système → Général → Time Machine → Ajouter un disque de sauvegarde.

Notez précieusement le mot de passe des sauvegardes restic : sans lui, aucune restauration n'est possible. Testez une restauration de temps en temps (bouton « Restaurer »).

## 10. Sauvegarde de la configuration

Vos réglages méritent aussi une sauvegarde. Dans Paramètres → **Sauvegarde de la configuration** :

- **Exporter** télécharge un fichier JSON avec les paramètres, comptes, dossiers partagés, tâches, apps, réglages réseau et de sécurité (pas vos fichiers). Avec « Inclure les secrets », les mots de passe, jetons et clés sont ajoutés, chiffrés avec une phrase secrète de votre choix : sans elle, ils sont irrécupérables.
- **Importer** restaure un tel fichier, par exemple sur un nouveau matériel ou après une réinstallation. Un **aperçu** montre, section par section, ce qui sera créé ou modifié ; vous choisissez ce qu'il faut récupérer. Rien de ce qui n'existe que sur ce NAS n'est supprimé, et les disques et volumes ne sont jamais touchés : associez chaque volume du fichier à un volume de ce NAS.
- **Sauvegarde automatique quotidienne** : une copie de la configuration chaque jour dans le dossier de votre choix, idéalement un dossier partagé lui-même sauvegardé.

## 11. Snapshots et versions précédentes

Sur Btrfs et ZFS, programmez des **snapshots** (Tâches planifiées → recommandé). Un snapshot **verrouillé** ne peut pas être supprimé pendant la durée choisie, même par un administrateur ou un rançongiciel.

Pour récupérer un fichier : dans **Fichiers**, bouton « Versions précédentes » sur le fichier ; sous Windows, Propriétés → Versions précédentes sur le partage.

## 12. Protéger le NAS

- **Anti-rançongiciel** (Antivirus) : surveille les renommages en masse sur SMB, WebDAV, SFTP, FTP et NFS et bloque le compte en cause. Activez aussi les **fichiers pièges** (`!0000 - DO NOT MODIFY (StrataOS).docx` et `zzzz - DO NOT MODIFY (StrataOS).xlsx`, placés en tête et en fin de liste dans chaque partage) : s'ils sont touchés, le partage passe en lecture seule. Après une alerte : vérifiez le poste, restaurez depuis les snapshots, puis débloquez le compte et rétablissez le partage.
- **Antivirus ClamAV** : analyses planifiées des partages.
- **Blocage d'adresses** : bloque les adresses qui ratent trop de connexions, et peut limiter l'accès depuis Internet à certains pays.
- **Dossiers chiffrés** : protègent les données si le NAS ou ses disques sont volés. Après chaque démarrage, un administrateur doit les déverrouiller (Dossiers partagés → Déverrouiller). Gardez la clé de secours en lieu sûr.
- **Clés d'accès** : connectez-vous avec votre empreinte, votre visage, le code PIN de votre appareil (Windows Hello, Touch ID, Android) ou une clé de sécurité, à la place du mot de passe et du code à deux facteurs. Ajoutez-en une depuis le menu de votre compte → Clés d'accès → « Ajouter une clé d'accès », puis utilisez « Se connecter avec une clé d'accès » sur la page de connexion. Les clés d'accès ne fonctionnent que si vous ouvrez le NAS **par son nom, en HTTPS** avec un certificat valide (par exemple `https://strataos.local:7443` ou votre propre domaine), pas par son adresse IP ; une clé ne fonctionne que sur le nom où elle a été créée.
- Gardez le système à jour (Mises à jour), automatiquement si vous le souhaitez (voir la section 20) : un point de restauration est créé avant chaque mise à jour.

## 13. Accès à distance

Le plus sûr est le **VPN WireGuard** (Accès à distance) : ajoutez un appareil, scannez le QR code avec l'application WireGuard, et vous accédez au NAS comme chez vous. Il faut rediriger le port UDP indiqué sur votre box et, si votre adresse change, activer le **DNS dynamique**. Évitez d'exposer directement l'interface web sur Internet ; si vous le faites, activez le blocage d'adresses et la double authentification.

- **Appareils** : chaque appareil appartient à un utilisateur et a son propre accès (le NAS seulement, le NAS et le réseau local, ou tout le trafic), une date d'expiration facultative et une restriction facultative à certains services. Si vous autorisez les utilisateurs à ajouter leurs propres appareils, chacun ajoute et révoque les siens dans **Mes appareils VPN** (dans le menu ou le menu du compte).
- **Sites** : reliez tout un réseau distant (un autre bureau, une résidence secondaire) au réseau local du NAS, via son routeur ou un autre StrataOS (avec un code de site).
- **Tunnel sortant** : le NAS se connecte à un fournisseur VPN avec le fichier de configuration qu'il vous donne. Faites passer seulement les apps Docker choisies (recommandé) ou tout le NAS ; un coupe-circuit (kill switch) prive les apps concernées (ou tout le NAS, sauf le réseau local et le serveur WireGuard) d'Internet si le tunnel tombe. L'IPv6 passe par le tunnel quand le fournisseur le donne, et est bloqué sinon. Les appareils ayant une date de fin sont signalés aux administrateurs quelques jours avant (7 par défaut).

**Autres moyens d'accès** (même page) :

- **Tailscale** : relie vos appareils dans un réseau privé sans ouvrir de port sur la box. Installez-le, connectez-vous avec un navigateur, puis installez Tailscale sur votre téléphone ou ordinateur avec le même compte. Il peut aussi partager votre réseau local ou servir de nœud de sortie.
- **Cloudflare Tunnel** : publie des services web sous votre domaine via Cloudflare, sans ouvrir aucun port. Collez le jeton du tunnel, puis ajoutez un nom d'hôte public par service dans le tableau de bord Cloudflare.
- **Domaines des apps** : donnez à vos apps des adresses propres comme `https://jellyfin.example.com`, avec un certificat Let's Encrypt gratuit par nom, renouvelé automatiquement. Redirigez les ports 80 et 443 vers le NAS. Pour les apps sans connexion propre, choisissez « Réseau local uniquement », ou passez par le VPN ou Tailscale.

Tout ce qui est publié sur Internet est accessible à tous : gardez la connexion et la double authentification activées.

## 14. Applications

L'**App Center** installe en un clic des applications prêtes dans Docker : médias (Jellyfin, Audiobookshelf, Kavita…), photos (Immich), domotique (Home Assistant, Node-RED, Mosquitto, Zigbee2MQTT), productivité (Nextcloud, OnlyOffice Docs, Actual Budget, Memos, Linkwarden, Vikunja), IA locale (Ollama, Open WebUI), supervision (Netdata, Dozzle, Speedtest Tracker) et bien d'autres. Leurs mises à jour peuvent être programmées.

**Mes apps** liste ce que vous avez installé. Vous pouvez aussi « Ajouter une app personnalisée » à partir de votre propre fichier compose, ou ajouter une stack Compose existante à Mes apps pour lui donner une icône et un bouton Ouvrir. N'utilisez que des fichiers compose de confiance : les conteneurs s'exécutent avec les droits que vous leur donnez.

**Mises à jour en un clic** : StrataOS vérifie toutes les 12 heures (ou avec « Rechercher les mises à jour » sur les pages Conteneurs, Stacks et App Center) si le registre (Docker Hub, GHCR ou un autre) propose une image plus récente pour l'étiquette qu'utilise chaque conteneur. Les conteneurs et les apps concernés affichent « Mise à jour disponible » et un bouton **Mettre à jour** ; « Tout mettre à jour » les fait tous. Une mise à jour télécharge la nouvelle image et recrée le conteneur avec exactement les mêmes réglages (ports, dossiers, variables, réseaux), ou relance la stack pour une app ; l'ancien conteneur et l'ancienne image sont gardés jusqu'à ce que le nouveau tourne, et s'il ne démarre pas, la version précédente est remise en place. Chaque nouvelle mise à jour est notifiée une fois. Limites : les images construites sur le NAS ou épinglées par un condensat (digest) ne peuvent pas être vérifiées, et les registres privés ne sont pas pris en charge (accès anonyme uniquement).

Pour **désinstaller** une app, utilisez le bouton corbeille de sa carte (« Désinstaller »). Ses conteneurs et réseaux sont supprimés, et « Supprimer ses images (sauf si un autre conteneur les utilise) » libère leur espace disque. Ses données (dossiers de l'app et volumes Docker) sont conservées pour une réinstallation ultérieure, sauf si vous cochez « Supprimer aussi ses données » ; les dossiers situés dans un dossier partagé ou utilisés par une autre app sont toujours conservés.

Les **composants facultatifs** (ZFS, Docker, machines virtuelles, antivirus, VPN, aperçus de documents…) se retirent dans Paramètres → **Composants** → « Désinstaller ». StrataOS indique d'abord ce qui gêne : certaines choses doivent partir avant (un pool ZFS à exporter, des apps ou des VM à supprimer…), d'autres peuvent être désactivées pour vous (« Les désactiver et désinstaller quand même »). Il montre ensuite les paquets supprimés (ceux dont StrataOS ou un autre composant a encore besoin sont conservés), les services arrêtés et les réglages modifiés. Les données du composant sont conservées, et retrouvées par une réinstallation ultérieure, sauf si vous cochez « Supprimer aussi ses données ».

## 15. Machines virtuelles

Les **machines virtuelles** (KVM) s'installent en un clic. « Créer une VM » propose trois façons :

- **Image cloud** : Debian ou Ubuntu, prête en SSH en une minute (utilisateur, clé SSH et paquets sont réglés au premier démarrage).
- **Installer depuis une ISO** : n'importe quel système, installé depuis la console dans le navigateur (Windows 11 reçoit une puce TPM virtuelle et le démarrage sécurisé).
- **Importer** : un disque OVA/OVF, VMDK, VHD(X), qcow2 ou brut venant d'un autre hyperviseur.

Chaque VM peut prendre des **Snapshots** (avec sa mémoire si elle tourne) et être **clonée**. Préparez une VM une fois, puis « Convertir en modèle » pour créer des VM identiques à partir d'elle (onglet Modèles). L'onglet **Périphériques USB** donne un périphérique USB (clé Zigbee, onduleur, clé de licence…) à une VM ; il revient après avoir été débranché. **Ordre de démarrage** choisit les VM qui démarrent avec le NAS, dans quel ordre et avec quel délai. **Sauvegardes** liste les VM sauvegardées par vos tâches de sauvegarde et en restaure une comme nouvelle VM.

Le **passthrough PCI** (activez « Avancé ») donne un périphérique entier, comme une carte graphique, à une VM. C'est réservé aux utilisateurs expérimentés : un mauvais périphérique peut rendre le NAS injoignable.

### Conteneurs (LXC)

La même application fait tourner des **conteneurs LXC** : des systèmes Linux légers qui partagent le noyau du NAS. Installez-les depuis l'application (ou Réglages → Composants), puis **Créer → Conteneur (LXC)** : choisissez une image (Debian 12 et 13, Ubuntu LTS, Alpine, Fedora, Arch, Rocky… téléchargée depuis images.linuxcontainers.org et vérifiée avec sa signature GPG), un nom, un volume et les limites. Conteneurs et VM sont dans une seule liste ; le filtre au-dessus montre l'un ou l'autre.

**Conteneur ou VM ?** Un conteneur démarre en une seconde et n'utilise presque pas de mémoire à lui, idéal pour des services Linux (un serveur web, Pi-hole, Home Assistant Core, une petite base de données, Docker). Prenez une VM pour Windows, un autre noyau, des modules du noyau, ou quand vous voulez l'isolation la plus forte.

- **Non privilégié** (par défaut) : les utilisateurs du conteneur sont décalés vers les numéros 100000 et plus du NAS, son root n'est donc personne sur le NAS. Les conteneurs **privilégiés** utilisent les numéros du NAS tels quels : ce qui s'en échappe contrôle le NAS. Réservez-les aux logiciels qui ne marchent pas autrement ; le choix ne peut pas changer ensuite.
- **Stockage** : sur un volume Btrfs, un sous-volume ; sur un dataset ZFS, un dataset ; ailleurs une **image disque** (un fichier ext4). Ces trois-là imposent la taille du disque. Un simple **dossier** marche partout mais sans limite de taille.
- **Limites** : processeur (en cœurs), mémoire et swap s'appliquent tout de suite, même à un conteneur qui tourne ; le disque peut grandir plus tard. À l'intérieur, `free` et `top` montrent les limites.
- **Réseau** : NAT (un réseau privé derrière le NAS, 10.0.3.0/24) ou un pont sur le réseau local (les mêmes ponts que les VM), en DHCP ou avec une adresse fixe. L'adresse s'affiche dans la liste. Les VLAN ne sont pas pris en charge (les VM non plus).
- Les **dossiers partagés** se montent à l'intérieur (en lecture seule ou en lecture-écriture). Dans un conteneur non privilégié, leurs fichiers apparaissent comme « nobody » : dans l'onglet **Dossiers partagés**, gardez le numéro de certains comptes ou groupes StrataOS, puis créez dans le conteneur des utilisateurs avec le même numéro (`useradd -u 1001 alice`) pour lire et écrire leurs fichiers.
- **Options** : *imbrication* et *keyctl* pour faire tourner Docker dedans, *FUSE*, *TUN* pour les logiciels VPN ; démarrage avec le NAS, dans l'ordre et avec un délai.
- L'onglet **Console** ouvre un shell root dans le navigateur, ou la console du conteneur (son invite de connexion ; donnez un mot de passe root à la création). **Ressources** trace les graphiques processeur, mémoire, réseau et disque.
- Les **snapshots** gardent le système de fichiers racine (pas la mémoire) ; on en restaure un conteneur arrêté. Sur ZFS, restaurer supprime les snapshots pris après. **Sauvegardes** écrit une archive `.tar.zst` dans un dossier partagé (depuis un snapshot si le conteneur tourne), et les tâches de sauvegarde peuvent inclure des conteneurs à côté des dossiers et des VM. **Importer une sauvegarde** (dans l'assistant de création) en restaure une sur ce NAS ou un autre, et ajoute aussi un dossier de conteneur trouvé sur un volume venu d'un autre NAS.

Les conteneurs continuent de tourner pendant une mise à jour ou un redémarrage de StrataOS, et ceux marqués « démarrer avec le NAS » repartent après un redémarrage. Le composant Conteneurs ne peut pas être désinstallé tant qu'il reste des conteneurs.

## 16. Surveillance

La page **Surveillance** enregistre des caméras réseau (RTSP ou MJPEG) avec l'enregistreur intégré.

1. Dans **Paramètres**, choisissez le dossier des enregistrements : rien n'est enregistré avant.
2. Dans **Caméras**, ajoutez une caméra avec l'adresse de son flux (voir le manuel de la caméra). Ajoutez le flux secondaire basse résolution s'il existe : la vue en direct est bien plus légère pour le NAS. Réglez combien de temps garder les enregistrements (jours, taille maximale) ; les plus anciens sont supprimés en premier.
3. **En direct** montre toutes les caméras à la fois ; **Enregistrements** permet de lire une journée, de télécharger un segment ou de découper un extrait.

Vous êtes prévenu quand une caméra est injoignable. L'enregistreur intégré ne détecte ni les mouvements ni les objets : pour cela, installez **Frigate** (Surveillance → Paramètres, Docker requis). Il détecte personnes, voitures et animaux, et StrataOS notifie ses événements ; une clé Coral USB ou un GPU Intel accélère la détection. Chaque caméra est enregistrée par l'un ou l'autre, pas les deux.

## 17. Alimentation et matériel

La page **Alimentation** met les disques durs en veille quand ils ne servent pas, choisit la politique du processeur, programme l'arrêt et le redémarrage (par exemple la nuit), et active le réveil par le réseau (Wake-on-LAN). Un arrêt programmé attend la fin d'une sauvegarde ou d'une mise à jour.

La page **Matériel** affiche les températures et les ventilateurs. Sous Contrôle des ventilateurs, choisissez « BIOS (automatique) », « Silencieux », « Équilibré », « Frais » ou une « Courbe personnalisée » ; les ventilateurs suivent le plus chaud du processeur et des disques. « Voyants de la face avant » allume ou éteint les voyants du NAS, ou leur fait montrer l'activité des disques, quand le matériel le permet. Sur les NAS **TerraMaster**, le contrôle des ventilateurs nécessite le pilote it87 hors arborescence (github.com/frankcrawford/it87), que StrataOS n'installe pas ; d'ici là, le BIOS continue de piloter les ventilateurs, ce qui est sans risque.

## 18. Haute disponibilité

Avec deux NAS ou plus sur le même réseau, l'un (le principal) sert les partages à une **adresse partagée** ; les autres reçoivent une copie des comptes, des partages et des fichiers (en continu ou périodiquement) et prennent le relais en quelques secondes si le principal tombe en panne. Les volumes des secondaires peuvent porter d'autres noms : indiquez la correspondance à l'appairage. Pour redémarrer le principal sans coupure, utilisez « Maintenance ». Après une panne, le principal ne reprend jamais la main seul : rendez-la-lui depuis le secondaire actif, qui recopie d'abord ses modifications. Pour des machines virtuelles relancées sur un autre NAS, voir la section 25.

## 19. Surveiller et être prévenu

- **Tableau de bord** et **Moniteur** : processeur, mémoire, réseau, disques.
- **Journaux** : tout ce qui s'est passé (connexions, tâches, alertes).
- **Notifications** (Paramètres → Notifications) : sous Livraison, activez ou coupez chaque canal (e-mail, Telegram, ntfy, webhook) et choisissez son niveau minimum : « Tout (informations et au-dessus) », « Avertissements et erreurs » ou « Erreurs uniquement ». Par exemple, les erreurs par e-mail et tout sur votre téléphone. Les **heures calmes** gardent la nuit tranquille : seules les erreurs sont envoyées (ou rien du tout, si vous le choisissez), le reste reste dans les journaux. Les opérations longues comme la reconstruction d'un RAID envoient une notification au début et une à la fin. Le bouton Tester ignore les niveaux et les heures calmes.
- **Rapport mensuel** (Paramètres → Notifications).
- **Prometheus / Grafana** (Paramètres → Supervision) pour un suivi dans la durée.
- **Console multi-NAS** pour voir l'état de plusieurs StrataOS d'un coup d'œil. Elle peut aussi les mettre à jour : sur chaque NAS, autorisez la console à installer les mises à jour (Console multi-NAS → Accès autorisés), puis sélectionnez les NAS et cliquez sur **Mettre à jour la sélection**, tous en même temps ou l'un après l'autre. Les deux NAS d'une paire en haute disponibilité sont toujours mis à jour l'un après l'autre.

## 20. Mises à jour et retour en arrière

Mises à jour → « Mettre à jour ». Avant chaque mise à jour, un **point de restauration** est créé ; si quelque chose ne va plus, « Restaurer » ramène le système et ses programmes à cet état (redémarrage), sans toucher à vos données ni aux réglages de StrataOS.

**Canal de mise à jour** : **Stable** (versions finales uniquement, recommandé) ou **Bêta**, qui installe aussi les versions bêta et les versions candidates dès leur publication. Les bêtas peuvent avoir des bugs : gardez des sauvegardes, et ne les utilisez pas sur un NAS dont vous ne pouvez pas vous passer un moment. De retour sur Stable, la bêta installée reste en place jusqu'à la publication d'une version stable plus récente, ou « Revenir à la dernière version stable » y retourne tout de suite (après un point de restauration).

**Mises à jour automatiques** : choisissez séparément pour **StrataOS**, les **Mises à jour de sécurité Debian** et les **Autres mises à jour du système** : « Désactivé », « Notifier seulement » ou « Installer automatiquement ». Elles s'exécutent dans la **Fenêtre de maintenance** de votre choix (jours et plage de 3 heures), après un point de restauration ; une sauvegarde, un scrub ou une autre tâche en cours les repousse. Activez « Redémarrer le NAS quand une mise à jour l'exige », sinon vous êtes simplement averti qu'un redémarrage est nécessaire.

**Raspberry Pi installé depuis une image antérieure à 0.6.1** (0.4.0 à 0.6.0) : il utilise le noyau de Debian, qui n'a pas de pilote pour les ports Ethernet et USB du Raspberry Pi 5. Mises à jour → **Passer au noyau Raspberry Pi** installe les noyaux et le firmware de démarrage Raspberry Pi de l'image actuelle (seuls ces paquets viennent du dépôt Raspberry Pi), garde ce que vous aviez ajouté à `config.txt` et `cmdline.txt`, et redémarre. Avant cela, un point de restauration est créé et une copie de la partition du firmware est gardée dans `/var/lib/strata-restore/rpi-kernel/`. Une fois le NAS démarré sur le nouveau noyau, StrataOS supprime le noyau Debian. S'il ne redémarre pas dans les 15 minutes, mettez la carte (ou le SSD) dans n'importe quel ordinateur et, sur sa première partition (la petite, lisible par tout ordinateur), renommez `config.txt` en `config-rpi.txt` et `config-debian.txt` en `config.txt` (voir `README-StrataOS.txt` sur la carte) : le Pi redémarre sur le noyau Debian, et **Annuler** sur la page Mises à jour remet tout en place. Il n'y a pas de retour automatique : l'essai de démarrage unique du firmware Raspberry Pi (« tryboot ») ne peut être demandé que depuis un noyau Raspberry Pi.

## 21. En cas de problème

- **L'interface ne répond plus** : vérifiez que le NAS est allumé et sur le réseau ; l'écran de la machine affiche son adresse. Après un mauvais réglage réseau, « Reset the network to DHCP » sur cet écran rétablit l'accès (section 2). Depuis une console, `systemctl status strata` montre l'état du service.
- **Un disque manque au démarrage** : la grappe RAID démarre en mode dégradé et une alerte est envoyée ; remplacez le disque.
- **Un partage n'apparaît plus** : il est peut-être chiffré et verrouillé, gelé par un fichier piège (Antivirus), ou plein (quota).
- **Compte bloqué** : un administrateur le débloque dans Antivirus (rançongiciel) ; une adresse bloquée se débloque dans Blocage d'adresses.
- **Pas de bouton de clé d'accès à la connexion** : ouvrez le NAS par son nom en HTTPS, pas par son adresse IP (voir 12).
- **Aucune notification reçue** : vérifiez que le canal est activé et que son niveau n'est pas trop élevé (Paramètres → Notifications), et que vous n'êtes pas dans les heures calmes ; utilisez le bouton Tester.
- **Mot de passe administrateur perdu** : sur la page de connexion, cliquez sur « Mot de passe oublié ? Utilisez un code de récupération affiché sur l'écran du NAS » et saisissez le code affiché par « Administrator recovery code » sur l'écran du NAS (section 2). Vous choisissez alors un nouveau mot de passe pour un administrateur, et pouvez « Supprimer aussi la double authentification (téléphone perdu) » ; les administrateurs sont prévenus. Sans écran, connectez-vous en console ou en SSH avec un autre administrateur ; sinon, démarrez sur une clé Debian et réinitialisez le mot de passe du compte Linux, puis changez-le dans l'interface.

## 22. L'application Android

L'**application StrataOS pour Android** (Android 8 et plus) parcourt vos fichiers, sauvegarde les photos et vidéos de votre téléphone et affiche les alertes du NAS. Elle n'a besoin d'aucun service Google.

1. **Installer** : sur le téléphone, téléchargez `strataos-<version>.apk` dans les Releases (aussi accessible par menu du compte → Téléphones et ordinateurs → « Télécharger l'application (APK) »), ouvrez-le et autorisez l'installation depuis cette source quand Android le demande.
2. **Se connecter** : dans l'interface web, menu du compte → **Téléphones et ordinateurs** → « Associer un téléphone », puis « Scanner un QR code » dans l'application : aucun mot de passe à taper (le code sert une fois, pendant 10 minutes). Vous pouvez aussi saisir l'adresse du NAS, ou le choisir sous « Trouvés sur ce réseau », et vous connecter avec votre mot de passe et votre code à deux facteurs. Hors de chez vous, passez par le VPN, Tailscale ou un nom de domaine (section 13).
3. **Vérifier le certificat** : si le NAS utilise son propre certificat, l'application affiche son « Empreinte du certificat (SHA-256) ». Comparez-la avec celle affichée dans Associer un téléphone, puis touchez « Faire confiance à ce NAS » : l'application n'accepte ensuite que ce certificat, et vous prévient s'il change.

L'application a trois onglets, **Fichiers**, **Photos** et **Gérer**, et un bouton de compte (en haut à droite) pour la **Sauvegarde**, la **Synchronisation de dossiers** et les **Réglages de l'app**. Sur une tablette, les onglets sont sur le côté, et Gérer affiche sa liste à côté de la partie ouverte.

- **Fichiers** : parcourir, rechercher (le dossier et tout ce qu'il contient), trier (nom, date, taille, type), vue en liste ou en grille avec miniatures, et « Propriétés ». Appuyez longuement pour sélectionner plusieurs éléments, puis « Copier vers… », « Déplacer vers… » (aussi d'un dossier partagé à un autre), « Supprimer », « Enregistrer sur le téléphone » ou « Renommer ». La suppression passe par la **Corbeille** du dossier partagé quand il en a une (⋮ → Corbeille, « Restaurer »). « Lien de partage » crée un lien public (téléchargement, dépôt de fichiers pour un dossier, mot de passe, expiration, nombre de téléchargements) ; ⋮ → **Liens de partage** liste les vôtres. Ajoutez fichiers et dossiers aux **Favoris** ; « Rendre disponible hors ligne » garde une copie sur le téléphone, mise à jour toutes les quelques heures en Wi-Fi quand le fichier change. Envoyez des fichiers avec le bouton + ; depuis n'importe quelle autre application, **Partager → StrataOS** envoie des fichiers dans le dossier du NAS de votre choix.
- **Vidéos et musique** se lisent dans l'application, diffusées par le NAS : les fichiers que le téléphone ne sait pas lire sont convertis à la volée. Le lecteur propose la qualité (Automatique, de 1080p à 360p pour une connexion lente, ou le fichier d'origine), la piste audio, les sous-titres et la vitesse, reprend là où vous vous étiez arrêté, continue la musique écran éteint (commandes dans la notification) et réduit une vidéo en image dans l'image quand vous quittez l'application. Les autres musiques du dossier suivent.
- **Photos** : la chronologie de l'application Photos du NAS par mois, la recherche, les albums (créer, ajouter ou retirer des photos, renommer, partager par un lien, supprimer), l'affichage plein écran (balayer, pincer ou toucher deux fois pour zoomer), les vidéos, « Enregistrer sur le téléphone » et la suppression (dans la corbeille quand le dossier partagé en a une). Le NAS n'a pas de favoris pour les photos : utilisez les albums.
- **Gérer** : l'état du NAS (santé, alertes, stockage) et les parties de gestion auxquelles vous avez droit. Tout le monde a les **Liens de partage** ; les administrateurs ont aussi :
  - **Stockage** : volumes (utilisation, santé, vérification, quotas par utilisateur, suppression), disques avec leur santé SMART (détails, test court ou long), grappes RAID (membres, avancement de la reconstruction, vérification), pools ZFS (scrub, destruction), instantanés (prendre, supprimer, revenir en arrière pour un jeu de données ZFS ; un instantané Btrfs s'ouvre dans Fichiers pour recopier des fichiers), dossiers partagés (créer, protocoles, corbeille, taille maximale, qui peut lire ou écrire, supprimer) et quotas par utilisateur. Créer un volume, une grappe RAID ou un pool ZFS ne propose que les disques libres, affiche la capacité utile, puis chaque disque qui sera effacé, et demande de taper le nom avant de faire quoi que ce soit. Supprimer demande la même chose.
  - **Applications** : l'App Center (recherche, catégories, installation avec les réglages de l'app, ouvrir, mettre à jour, désinstaller avec ou sans ses données), les conteneurs (démarrer, arrêter, redémarrer, journaux en direct, processeur et mémoire, supprimer), les stacks et les images (télécharger, supprimer), avec les badges **Mise à jour disponible**, « Rechercher les mises à jour » et « Tout mettre à jour » de l'interface web.
- **Sauvegarde** : activez « Sauvegarde automatique », choisissez la « Destination sur le NAS » et les albums, avec en option « Wi-Fi uniquement », « Seulement pendant la recharge » et « Inclure les vidéos ». Les envois reprennent après une coupure, les fichiers déjà sur le NAS sont ignorés, et rien n'est jamais supprimé du téléphone.
- **Synchronisation de dossiers** : garde un dossier du téléphone et un dossier du NAS identiques, comme l'application de synchronisation pour ordinateur (section 23). « Ajouter une paire », choisissez le dossier du NAS (un dossier partagé ou un dossier à l'intérieur), puis le dossier du téléphone : n'importe quel dossier du téléphone ou d'une carte mémoire (« Choisir un dossier », l'application garde l'autorisation), ou un « Dossier de l'application » (dans Android/data, supprimé avec l'application). Choisissez le sens : **Dans les deux sens**, **Téléphone → NAS** (le téléphone n'est jamais modifié) ou **NAS → téléphone** (le NAS n'est jamais modifié), et quand : « Wi-Fi uniquement », « Seulement pendant la recharge ». L'application synchronise toutes les 15 minutes au mieux (Android choisit le moment exact), quelques secondes après un changement sur le NAS quand l'application est ouverte, toutes les deux minutes pour le dossier du téléphone quand elle est ouverte, et avec « Synchroniser maintenant » (qui ignore les conditions de Wi-Fi et de recharge). Rien ne tourne pendant l'économiseur de batterie, et une notification s'affiche pendant les transferts.
  - Un fichier modifié des deux côtés garde les deux versions : celle du téléphone devient « nom (conflict from <téléphone> <date>).ext » à côté (Téléphone → NAS : elle part sur le NAS sous ce nom, le fichier du téléphone reste tel quel). La paire liste ses conflits.
  - Supprimé sur le téléphone : le fichier du NAS va dans la corbeille du dossier partagé (s'il en a une). Supprimé sur le NAS : le fichier du téléphone va dans le dossier **.trash** du dossier du téléphone et y reste 30 jours (Android n'a pas de corbeille pour ces dossiers).
  - Quand un passage supprimerait beaucoup de fichiers (au moins 20, et plus de la moitié de la paire), il attend : une notification propose « Les supprimer » ou « Les restaurer ».
  - Les transferts interrompus reprennent ; les noms que le téléphone refuse (`" * : < > ? \ |`), les liens symboliques et deux noms du NAS qui ne diffèrent que par les majuscules sont ignorés et listés dans la paire.
  - Limites : dans un dossier choisi avec « Choisir un dossier », Android ne donne ni identifiant de fichier ni moyen de fixer les dates : un fichier renommé sur le téléphone est renvoyé (l'ancien nom va dans la corbeille) et les fichiers téléchargés portent la date de leur téléchargement. Les renommages sur le NAS, et dans un dossier de l'application, se font sans rien transférer.
- **Notifications** (administrateurs) : installez l'application **ntfy** sur le téléphone comme distributeur UnifiedPush, puis « Activer les notifications instantanées » dans les Réglages de l'application ; sans elle, l'application vérifie le NAS toutes les 15 minutes. Choisissez le niveau du canal « Application StrataOS (téléphones) » dans Paramètres → Notifications, comme pour les autres canaux.
- **Verrouillage** : « Déverrouiller par empreinte ou verrouillage d'écran » protège l'application sur un téléphone partagé.

Téléphone perdu ou remplacé ? Menu du compte → Téléphones et ordinateurs → « Révoquer » : l'application de ce téléphone est déconnectée immédiatement et cesse de sauvegarder.

### Gérer le NAS depuis le téléphone

L'onglet **Gérer** rassemble les espaces d'administration. Chaque liste se rafraîchit en la tirant vers le bas, et supprimer, redémarrer ou éteindre demande confirmation (pour les actions les plus destructrices, en tapant le nom de ce qui disparaît). Les espaces des administrateurs sont masqués pour les autres utilisateurs.

- **Surveillance** : les caméras avec leurs images en direct ; touchez-en une pour sa **vidéo en direct** (le NAS reconditionne le flux H.264 ou H.265 de la caméra sans le convertir, avec une ou deux secondes de décalage ; tournez le téléphone pour le plein écran, « Pleine qualité » bascule entre le flux principal et le flux secondaire). Les caméras qui envoient du MJPEG en HTTP, ou un codec que le téléphone ne sait pas décoder, affichent des images en direct à la place. **Enregistrements** : un jour à la fois, touchez la frise pour lire depuis ce moment, enregistrez ou partagez un fichier entier, ou exportez un extrait (copié sans réencodage). **Événements** (caméras enregistrées par Frigate) : ce qui a été détecté, filtré par objet, avec capture et extrait. Les réglages de chaque caméra (flux, identifiants, enregistreur, son, rétention, test de connexion) et ceux de la Surveillance (dossier des enregistrements, alerte hors ligne, rétention, détecteur et objets de Frigate, sa configuration générée) y sont aussi. Les zones de détection ne sont pas disponibles (pas plus que dans l'interface web).
- **Machines virtuelles** : état, ressources et utilisation du processeur, démarrer, arrêter, redémarrer, suspendre, forcer l'arrêt ; le détail avec les instantanés (créer, restaurer, supprimer), les ressources (VM arrêtée), le CD-ROM et le démarrage avec le NAS ; un **assistant de création** en trois étapes (ISO ou image cloud, ressources et réseau, récapitulatif) ; et la **console** : touchez pour cliquer, appui long pour un clic droit, deux doigts pour zoomer et déplacer, le bouton clavier pour taper, Ctrl+Alt+Suppr et les touches spéciales dans la barre, « Mode glisser » pour maintenir le bouton gauche. La console ne demande pas de mot de passe : elle passe par le NAS avec l'accès de l'application.
  Les **conteneurs LXC** sont listés avec les VM (« Tout », « Machines virtuelles », « Conteneurs ») : état, image, adresses, démarrer, arrêter, redémarrer, forcer l'arrêt ; le détail avec les ressources (utilisation du processeur et de la mémoire, limites appliquées tout de suite), le réseau, les dossiers partagés montés, les instantanés (créer, restaurer, supprimer), les sauvegardes dans un dossier du NAS (sauvegarder maintenant, liste), les options (fonctions, démarrage avec le NAS), cloner, renommer et supprimer ; un **assistant de création** en quatre étapes (image, ressources et stockage, réseau, dossiers et options) ; et un **terminal** : « Shell » (root, sans mot de passe) ou « Console » (l'invite de connexion), avec Ctrl, Échap, Tab, les flèches et d'autres touches dans une barre au-dessus du clavier ; faites défiler vers le haut pour revoir les lignes passées. Les comptes gardés dans un conteneur, l'import d'une sauvegarde et les sauvegardes planifiées restent dans l'interface web.
- **VPN** : le serveur WireGuard (marche/arrêt, adresse, accès des nouveaux appareils…), ses appareils avec leur dernier contact, activer/désactiver, expiration, révoquer, et **Ajouter un appareil** : l'application affiche le QR code à scanner avec l'application WireGuard de l'autre appareil, ou partage le fichier `.conf`. Aussi l'état du VPN sortant (marche/arrêt, vérifier l'adresse publique, reconnecter ; sa configuration reste dans l'interface web). Il n'y a pas d'OpenVPN. Les utilisateurs qui ne sont pas administrateurs y voient leurs propres appareils VPN, si l'administrateur le permet.
- **Utilisateurs et groupes** : créer, modifier (rôle, groupes, SSH, Docker, activé), réinitialiser un mot de passe (au moins 8 caractères), retirer une double authentification, quotas par volume, accès à chaque dossier partagé, supprimer ; les groupes et leurs membres.
- **Multi-NAS** : les NAS de la console avec leur santé, leurs versions et leurs mises à jour en attente ; ouvrir l'un d'eux dans le navigateur ; en sélectionner plusieurs pour rechercher les mises à jour ou lancer une **mise à jour groupée** (un par un ou en parallèle, avec sa progression) ; les consoles autorisées à surveiller ce NAS, et un code pour en autoriser une nouvelle.
- **Réglages du NAS** : mises à jour (rechercher, installer, canal, mises à jour automatiques et leur fenêtre), notifications (canaux, niveaux, heures calmes, test), résumé du réseau, services de fichiers (SMB, NFS, SSH, WebDAV, FTP, SFTP), points de restauration, redémarrer et éteindre.
- **Interface web** : l'interface web complète du NAS dans l'application, pour tout le reste, déjà connectée. Les téléchargements et les envois fonctionnent, et le certificat est vérifié comme le fait l'application. Cette session est liée au téléphone : révoquer le téléphone la ferme. Depuis le téléphone (l'application ou l'interface web qu'elle affiche), vous ne pouvez pas changer votre propre mot de passe, la double authentification, les clés d'accès ni les appareils associés : faites-le depuis un navigateur, pour qu'un téléphone perdu ne puisse pas vous enfermer dehors.

## 23. L'application de synchronisation pour ordinateur

**StrataOS Sync** garde des dossiers d'un ordinateur et des dossiers du NAS identiques, dans les deux sens, en arrière-plan, comme Synology Drive Client : un fichier ajouté, modifié, renommé ou supprimé d'un côté est vite pareil de l'autre. Les fichiers sont vraiment sur l'ordinateur (pas de fichiers fantômes), ils s'ouvrent donc aussi sans réseau ; les modifications faites entre-temps partent dès que le NAS est de nouveau joignable.

**Installer** (liens de téléchargement dans l'interface web : menu du compte → **Téléphones et ordinateurs** → Application pour ordinateur, ou la page Releases) :

- **Windows** 10 et 11 : `strataos-sync-<version>-windows-x64.msi` s'installe pour votre compte seulement (pas besoin des droits d'administrateur). L'application n'est pas signée : si SmartScreen vous prévient, choisissez « Informations complémentaires » → « Exécuter quand même ». Un `.zip` portable est aussi disponible.
- **macOS** 11 et suivants (Intel et Apple silicon) : ouvrez `strataos-sync-<version>-macos.dmg` et glissez **StrataOS Sync** dans Applications. L'application n'est pas signée : la première fois, clic droit dessus → **Ouvrir** → Ouvrir (ou Réglages Système → Confidentialité et sécurité → **Ouvrir quand même**).
- **Linux** : `strataos-sync-<version>-linux-amd64.deb` (ou `-arm64.deb`) pour Debian et Ubuntu (`sudo apt install ./strataos-sync-…deb`), ou le `.tar.gz` ailleurs. L'icône apparaît dans la zone de notification (sous GNOME, avec l'extension « AppIndicator » ; sans elle, ouvrez les réglages depuis le menu des applications).

**Associer l'ordinateur** : au premier lancement, l'application ouvre ses réglages dans votre navigateur. Dans l'interface web du NAS, menu du compte → **Téléphones et ordinateurs** → « Associer un ordinateur », puis « Copier le lien d'association » et collez-le dans l'application (onglet Compte) ; ou saisissez l'adresse du NAS (ou choisissez « Chercher sur le réseau ») et le code d'association, ou connectez-vous avec votre nom d'utilisateur et votre mot de passe (et votre code à deux facteurs). Si le NAS utilise son propre certificat, l'application affiche son empreinte : vérifiez que c'est celle affichée par « Associer un ordinateur » (le lien la contient), puis cochez « Les empreintes correspondent ». L'application n'accepte ensuite que ce certificat et refuse de se connecter s'il change (sauf s'il vient d'une autorité publique, comme Let's Encrypt).

**Choisir les dossiers** (onglet Dossiers) : un dossier du NAS (un dossier partagé ou un dossier à l'intérieur) et un dossier de l'ordinateur, autant de paires que vous voulez. Les fichiers déjà présents des deux côtés sont fusionnés : les fichiers identiques ne sont pas recopiés, les différents sont gardés tous les deux (voir plus bas). Sous chaque paire, « Sous-dossiers non synchronisés » écarte les dossiers du NAS dont vous ne voulez pas sur cet ordinateur (leurs fichiers restent sur le NAS). Réglages : le nom de l'ordinateur, les limites d'envoi et de téléchargement, les fichiers jamais synchronisés (par défaut `.DS_Store`, `Thumbs.db`, `desktop.ini`, les fichiers de verrouillage d'Office `~$*`, `*.tmp`, `.~lock*`), le démarrage à l'ouverture de session (activé par défaut) et la langue.

L'icône montre l'état (à jour, synchronisation, en pause, erreur) et son menu ouvre un dossier synchronisé ou l'interface web du NAS, met en pause ou reprend, et ouvre les réglages.

**Conflits** : quand le même fichier a changé des deux côtés avant d'avoir pu être synchronisé, rien n'est écrasé : la version du NAS garde le nom et la vôtre devient `nom (conflict from <ordinateur> <date>).ext`, des deux côtés. Une notification vous prévient ; comparez et gardez ce qu'il vous faut.

**Suppressions** : elles vont dans la corbeille de l'ordinateur, ou dans la corbeille du dossier partagé sur le NAS quand il en a une (son option « Corbeille réseau » ; sinon le fichier est supprimé sur le NAS). Par sécurité, quand un passage supprimerait plus de la moitié des fichiers d'un dossier (au moins 20), l'application attend : dans son onglet État, choisissez « Les supprimer » ou « Les garder (restaurer) » (les fichiers reviennent depuis l'autre côté). Un dossier local absent (un disque débranché) ou illisible ne compte jamais comme supprimé.

L'application suit vos droits : dans un dossier partagé en lecture seule, les modifications locales sont refusées avec un message clair (et gardées sur l'ordinateur) ; un dossier partagé plein ou gelé passe aussi en lecture seule. Ordinateur perdu ? Menu du compte → Téléphones et ordinateurs → « Révoquer » (administrateurs : Utilisateurs → Appareils) : l'ordinateur est déconnecté immédiatement, ses fichiers y restent.

Limites : les liens symboliques ne sont pas synchronisés (ils sont listés comme ignorés), ni les noms que l'autre côté refuse (sous Windows : `< > : " | ? * \`, les noms finissant par un point ou une espace, `CON`, `NUL`… ; sur le NAS : `\`) ; deux noms du NAS qui ne diffèrent que par les majuscules ne gardent que le premier sous Windows et macOS (l'autre est listé). Sous macOS, une modification à l'intérieur d'un fichier existant est vue en moins d'une minute (créations, renommages et suppressions tout de suite). L'application a été testée avec des dizaines de milliers de fichiers par dossier ; elle garde son état en mémoire, des centaines de milliers de fichiers demandent donc quelques centaines de Mo.

**Mode sans interface** (serveurs, scripts) : `strataos-sync pair --server https://nas:7443 --code <code> --fingerprint <empreinte>`, puis `strataos-sync add --local ~/Documents --remote /srv/strata/main/Documents`, et `strataos-sync daemon` (ou `sync-once` depuis une tâche planifiée). `strataos-sync --help` liste tout. Les réglages et l'état sont dans `~/.config/StrataOS Sync` (Linux), `~/Library/Application Support/StrataOS Sync` (macOS) ou `%AppData%\StrataOS Sync` (Windows) ; `config.json` contient le jeton de l'appareil, lisible par votre compte seulement.

## 24. Ceph

**Ceph** répartit les données sur les disques de plusieurs NAS et garde chaque morceau sur plusieurs d'entre eux (3 copies par défaut, sur 3 NAS différents) : un disque, ou un NAS entier, peut s'arrêter sans perte de données et sans interruption. StrataOS installe Ceph depuis Debian (Paramètres → Composants → **Ceph**) et le pilote lui-même ; il peut aussi utiliser un cluster Ceph géré ailleurs.

**Quand est-ce utile** : à partir de **3 NAS** (en dessous, il n'y a aucune tolérance aux pannes : StrataOS ne le permet qu'en « mode test »), sur un réseau rapide (**10 Gb/s conseillé** ; 1 Gb/s fonctionne mais limite les écritures, qui vont vers chaque copie), avec environ **4 Go de RAM par disque** confié à Ceph en plus de ce que le NAS utilise déjà (StrataOS réduit les caches sur les NAS de moins de 6 Go, ce qui est plus lent). Pour un seul NAS, le RAID, Btrfs ou ZFS restent plus simples et plus rapides ; pour une copie de secours, utilisez la haute disponibilité (section 18).

**Créer un cluster** : installez le composant Ceph sur chaque NAS. Sur le premier, Stockage → **Ceph** → « Créer un cluster Ceph » : donnez-lui un nom et choisissez le réseau que partagent les NAS (éventuellement un second réseau pour les copies entre NAS). Sur chacun des autres, « Rejoindre un cluster » affiche un code à usage unique : saisissez son adresse et son code sur le premier NAS. Choisissez ensuite les disques confiés à Ceph sur chaque NAS (disques vierges ; ceux qui contiennent d'anciennes données sont effacés après que vous avez tapé le nom du cluster ; le disque système et les disques des volumes StrataOS ne sont jamais proposés) et ce qu'il faut créer : un **pool RBD** (périphériques en mode bloc) et/ou un système de fichiers **CephFS**. StrataOS met en place les moniteurs (sur 3 NAS), les gestionnaires, les disques (BlueStore) et les pools ; les groupes de placement sont dimensionnés automatiquement.

**La page Ceph** montre l'état de santé (avec une explication de chaque avertissement), la capacité, les disques et services de chaque NAS, les groupes de placement, la reconstruction et le trafic des clients. Un changement d'état est notifié comme les autres alertes. Depuis cette page :

- **Ajouter un disque**, ou en **Retirer** un : Ceph recopie d'abord ses données sur les autres disques, puis le disque est effacé. **Remplacer** met un nouveau disque à la place d'un disque en panne (il garde son numéro).
- Le **mode maintenance** empêche Ceph de déplacer les données pendant que vous redémarrez un NAS.
- **Ajouter à Ceph** un autre membre du cluster, ou retirer un NAS qui n'a plus de disque.
- Les **clés clientes** donnent à un autre NAS ou ordinateur l'accès à un seul pool ou système de fichiers.

**Utiliser Ceph comme stockage** (Ceph → Stockage de ce NAS, aussi pour un cluster externe : saisissez les adresses de ses moniteurs, un utilisateur Ceph et sa clé, qui reste sur le NAS, lisible par le système seul ; « Tester la connexion » les vérifie d'abord) :

- **CephFS** devient un volume du NAS : créez-y des dossiers partagés comme sur n'importe quel volume. Plusieurs NAS peuvent monter le même CephFS en même temps.
- Une **image RBD** devient un volume formaté en ext4, XFS ou Btrfs, pour **un seul NAS** : la monter sur deux NAS à la fois la corromprait, StrataOS refuse donc une image déjà utilisée par un autre client.
- **Stockage des VM** : un pool RBD où vivent les disques des machines virtuelles ; il apparaît dans « Emplacement des disques » quand vous créez une VM depuis un ISO.

StrataOS monte les volumes Ceph dès que le cluster répond : un NAS démarre donc normalement même quand les autres sont éteints.

**Limites** : Ceph a besoin de la majorité du cluster : avec 3 NAS, un seul peut s'arrêter ; si deux s'arrêtent, les données attendent leur retour. Les images cloud et les VM importées ne peuvent pas encore aller sur Ceph (seules les VM installées depuis un ISO). Les instantanés et sauvegardes des volumes Ceph passent par les outils habituels du volume (instantanés Btrfs, tâches de sauvegarde), pas par Ceph. Les réseaux d'un cluster existant ne peuvent pas être changés depuis l'interface.

## 25. Cluster de NAS pour les machines virtuelles

Les NAS d'un cluster (Stockage → Ceph → le cluster de NAS, section 24) peuvent gérer leurs machines virtuelles ensemble, comme Proxmox : chaque NAS affiche toutes les VM et tous les conteneurs, une VM passe d'un NAS à l'autre sans s'arrêter, et les VM en **haute disponibilité** redémarrent sur un autre NAS quand le leur tombe en panne.

**Ce qu'il faut** : le composant Machines virtuelles sur chaque NAS du cluster ; **3 NAS** pour la haute disponibilité (à deux, aucun ne peut distinguer la panne de l'autre de son propre isolement) ; un stockage **Ceph** pour les disques des VM (Stockage → Ceph → Stockage des VM, ajouté sur **chaque** NAS, depuis le Ceph du cluster) ; les mêmes ponts réseau sur chaque NAS (mêmes noms, sur le même réseau), car une VM garde son interface réseau en changeant de NAS. Un réseau rapide entre les NAS aide : une migration à chaud copie toute la mémoire de la VM.

**L'onglet Cluster** (application Machines virtuelles, visible quand le NAS est dans un cluster) : les NAS et leur état (en ligne, maintenance, le meneur qui décide des reprises), le quorum, et toutes les VM et tous les conteneurs de chaque NAS avec leur état et leurs ressources. Démarrer, arrêter, ouvrir la console (elle passe par le NAS que vous utilisez), migrer, haute disponibilité. **Processeur des nouvelles VM** : « Processeur de l'hôte » est le plus rapide, mais une VM allumée ne peut alors aller que sur un NAS au processeur identique ; « Compatible cluster » choisit le modèle de processeur le plus récent que tous les NAS savent faire tourner, utilisé par les VM créées ensuite. Pour une VM existante, éteignez-la et utilisez le bouton processeur de sa ligne.

**Migrer une VM** (bouton flèches) : choisissez le NAS de destination ; StrataOS vérifie d'abord qu'il répond, qu'il a assez de mémoire libre, qu'il sait faire tourner le processeur de la VM, qu'il a les mêmes ponts (ou vous laisse en choisir un autre), le même stockage Ceph, et qu'aucune image de CD-ROM ni aucun périphérique USB/PCI n'attache la VM à ce NAS (éjectez-les d'abord ; une VM avec des instantanés ne peut pas partir). Une VM allumée sur Ceph part **à chaud** : elle continue de tourner pendant la copie de sa mémoire, puis reprend sur la destination en moins d'une seconde. Une VM dont les disques sont sur le NAS lui-même peut partir aussi, avec « Copier les disques », bien plus lentement (tout le disque voyage pendant que la VM tourne). Une VM éteinte sur Ceph déplace juste sa définition. La migration est une tâche que vous pouvez annuler (la VM continue alors là où elle était). Les échanges entre NAS sont chiffrés avec des certificats que StrataOS crée pour le cluster, sans aucun mot de passe.

**Les conteneurs** (LXC) se déplacent à l'arrêt : le conteneur est arrêté, copié sur l'autre NAS au format des sauvegardes, démarré là-bas s'il tournait, puis supprimé ici.

**Haute disponibilité** (bouton cœur) : activez-la pour une VM dont tous les disques sont sur le Ceph du cluster, et choisissez si vous voulez un NAS préféré. Les NAS du cluster se surveillent toutes les 5 secondes. Quand l'un ne répond plus pendant environ 45 secondes et que la majorité du cluster est d'accord, le meneur l'**isole** (Ceph refuse toute nouvelle écriture de ses anciens processus de VM) et ses VM en haute disponibilité qui tournaient redémarrent sur le NAS préféré, ou sur celui qui a le plus de mémoire libre. Un NAS qui perd le contact avec la majorité du cluster arrête lui-même ses VM en haute disponibilité au bout de 30 secondes, avant que les autres ne les relancent. Quand le NAS en panne revient, il supprime son ancienne copie des VM au lieu de les démarrer : une VM ne tourne jamais deux fois avec le même disque. Les VM en haute disponibilité qui tournaient redémarrent après un redémarrage de leur NAS, et avec le NAS si « Démarrer avec le NAS » est coché.

**Maintenance** (bouton de chaque NAS) : ses VM allumées sur Ceph partent à chaud vers les autres NAS, et aucune VM n'y est placée jusqu'à la fin de la maintenance. À utiliser avant une mise à jour ou un redémarrage. Les mises à jour groupées de la console Multi-NAS ne redémarrent jamais deux NAS du même cluster en même temps, et un NAS du cluster se vide ainsi avant son redémarrage (la maintenance se termine toute seule à son retour).

**Différence avec la haute disponibilité des fichiers** (section 18) : celle-ci associe deux NAS pour les dossiers partagés (une copie des données et une adresse partagée). Le cluster de VM partage les disques des VM grâce à Ceph et redémarre des VM ; les deux peuvent servir ensemble.

**Limites** : une VM redémarrée par la haute disponibilité redémarre vraiment (le contenu de sa mémoire est perdu, comme après une coupure de courant). Seuls les disques Ceph sont protégés ; les VM avec des disques sur un NAS ou des périphériques USB ou PCI transmis ne peuvent pas être en haute disponibilité. Si tout le réseau tombe, chaque NAS arrête ses VM en haute disponibilité et aucun ne les relance tant que la majorité du cluster ne se voit pas de nouveau. Les conteneurs ne peuvent ni partir à chaud ni être en haute disponibilité. Une VM déplacée éteinte garde ses réglages UEFI ; une VM migrée avec copie des disques ne perd rien mais prend du temps. Jusqu'à 16 NAS pour les migrations de VM.
