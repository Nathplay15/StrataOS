# Guide d'utilisation de StrataOS

Ce guide explique, étape par étape, comment installer StrataOS et s'en servir au quotidien. Chaque page de l'interface rappelle aussi l'essentiel ; ce guide donne l'ordre logique et les bonnes pratiques.

## 1. Installer StrataOS

**Sur un PC ou un serveur (amd64)** : téléchargez `strataos-<version>-amd64.iso` dans les Releases, écrivez-la sur une clé USB (balenaEtcher, Rufus en mode DD, ou `dd`), démarrez la machine sur la clé et choisissez « Install StrataOS » dans le menu de démarrage. L'installation est automatique et se déroule en anglais, avec un clavier américain et le fuseau horaire UTC par défaut (l'interface web a son propre réglage de langue) ; si la machine a plusieurs disques, l'installateur demande lequel accueille le système (les autres restent intacts pour vos données). Comptez 10 à 20 minutes.

**Sur une carte ARM 64 bits (arm64)** : même chose avec `strataos-<version>-arm64.iso` sur une machine qui démarre en UEFI.

**Sur un Raspberry Pi 4, 400, CM4 ou 3 B / 3 B+** : écrivez `strataos-<version>-raspberrypi.img.xz` sur une carte microSD ou un SSD USB (Raspberry Pi Imager → « Utiliser une image personnalisée », ou balenaEtcher), branchez le Pi au réseau par câble et démarrez-le. Au premier démarrage, la partition système s'agrandit toute seule ; patientez deux à trois minutes. L'image est en 64 bits. Avec 1 Go de mémoire, un Pi 3 convient au partage de fichiers simple, pas aux applications Docker, à ZFS ni aux machines virtuelles. Le Raspberry Pi 5 n'est pas encore pris en charge.

**Sur un Debian 13 existant** : `sudo apt install ./strataos_<version>_<arch>.deb`.

À la fin, l'écran de la machine affiche l'adresse de l'interface : `http://<adresse>:7070` (HTTPS : port 7443).

## 2. Premier démarrage

Ouvrez l'adresse dans un navigateur : l'assistant demande la langue, le nom du NAS et le compte administrateur (ce compte peut aussi utiliser `sudo` en SSH). Il propose ensuite de préparer le stockage, un premier partage, des alertes et des tâches recommandées (vérification des disques, snapshots).

Conseil : activez tout de suite les **notifications** (Paramètres → Notifications : e-mail, Telegram, ntfy ou webhook) pour être prévenu d'une panne de disque. Sur une nouvelle installation, les notifications sont en anglais par défaut ; changez leur langue au même endroit (Langue des notifications).

## 3. Le stockage

Le stockage s'organise en couches : **disques → grappe RAID ou pool → volume → dossiers partagés**.

1. **Disques** : vérifiez l'état SMART de chaque disque. Un disque « qui faiblit » doit être remplacé bientôt.
2. **Redondance** : choisissez une protection contre la panne d'un disque :
   - grappe **RAID mdadm** (RAID 1 avec 2 disques, RAID 5 avec 3 ou plus, RAID 6 avec 4 ou plus), puis un volume ext4 ou XFS dessus ;
   - ou un volume **Btrfs** sur plusieurs disques (profil RAID 1 ou 10) ;
   - ou un **pool ZFS** (Stockage → ZFS, installation en un clic), miroir ou RAIDZ.
3. **Volume** : il est monté dans `/srv/strata/<nom>`. Un volume sur un seul périphérique peut recevoir un **cache SSD** dès sa création.

Le RAID ne remplace pas une sauvegarde : il protège d'une panne de disque, pas d'une suppression, d'un rançongiciel ou d'un incendie.

Pour remplacer un disque en panne : Stockage → RAID (ou ZFS), « Remplacer », choisissez le nouveau disque ; la reconstruction démarre seule.

## 4. Utilisateurs et dossiers partagés

1. **Utilisateurs** : créez un compte par personne, regroupez-les en **groupes** (ex. « famille », « compta »). Activez la double authentification pour les administrateurs.
2. **Dossiers partagés** : créez un partage par usage (Documents, Photos, Médias…), sur le volume de votre choix, puis donnez les droits : lecture et écriture, lecture seule ou aucun accès, par utilisateur ou par groupe.
3. Options utiles : **corbeille réseau** (les fichiers supprimés sont gardés dans `.recycle`), **quota** du partage (passage en lecture seule une fois plein si vous le souhaitez), **chiffrement** (voir 9).

## 5. Accéder aux fichiers

- **Windows** : dans l'explorateur, tapez `\\<adresse>\<partage>` ; connectez-vous avec votre compte StrataOS.
- **macOS** : Finder → Aller → Se connecter au serveur → `smb://<adresse>/<partage>`. Un partage peut servir de cible **Time Machine**.
- **Linux** : `smb://` dans le gestionnaire de fichiers, ou NFS si le partage l'autorise.
- **Navigateur** : l'application **Fichiers** permet d'envoyer, télécharger (dossiers en ZIP), renommer, prévisualiser.
- **Téléphone, autres appareils** : activez WebDAV, SFTP ou FTP dans Services de fichiers.

Pour **partager avec quelqu'un sans compte**, utilisez « Partager un lien » sur un fichier ou un dossier (mot de passe, expiration, nombre de téléchargements, ou boîte de dépôt pour recevoir des fichiers). Retrouvez et supprimez vos liens dans **Liens partagés**.

## 6. Photos

Un administrateur choisit les dossiers de photos (Photos → Dossiers). La chronologie affiche photos et vidéos par mois ; chacun ne voit que les partages auxquels il a accès. Sélectionnez des photos pour créer des **albums**, partageables par lien.

## 7. Sauvegarder

Gardez au moins une copie de vos données ailleurs que sur le NAS (règle 3-2-1 : 3 copies, 2 supports, 1 hors site).

- **Copie rsync** vers un disque USB ou un serveur SSH.
- **Sauvegarde versionnée restic** (chiffrée, dédupliquée) vers un dossier, un serveur SFTP, du stockage S3 ou un **coffre d'un autre StrataOS** : sur l'autre NAS, créez un coffre (Sauvegarde → Coffres de sauvegarde, idéalement en « ajout seul ») et affichez un code ; sur ce NAS, choisissez la destination « Coffre d'un autre NAS StrataOS » et saisissez le code.
- **Réplication ZFS** vers un autre pool.
- **Synchronisation cloud** (Google Drive, OneDrive, Dropbox, S3…).

Notez précieusement le mot de passe des sauvegardes restic : sans lui, aucune restauration n'est possible. Testez une restauration de temps en temps (bouton « Restaurer »).

## 8. Snapshots et versions précédentes

Sur Btrfs et ZFS, programmez des **snapshots** (Tâches planifiées → recommandé). Un snapshot **verrouillé** ne peut pas être supprimé pendant la durée choisie, même par un administrateur ou un rançongiciel.

Pour récupérer un fichier : dans **Fichiers**, bouton « Versions précédentes » sur le fichier ; sous Windows, Propriétés → Versions précédentes sur le partage.

## 9. Protéger le NAS

- **Anti-rançongiciel** (Antivirus) : surveille les renommages en masse sur SMB, WebDAV, SFTP, FTP et NFS et bloque le compte en cause. Activez aussi les **fichiers pièges** (`!0000 - DO NOT MODIFY (StrataOS).docx` et `zzzz - DO NOT MODIFY (StrataOS).xlsx`, placés en tête et en fin de liste dans chaque partage) : s'ils sont touchés, le partage passe en lecture seule. Après une alerte : vérifiez le poste, restaurez depuis les snapshots, puis débloquez le compte et rétablissez le partage.
- **Antivirus ClamAV** : analyses planifiées des partages.
- **Blocage d'adresses** : bloque les adresses qui ratent trop de connexions, et peut limiter l'accès depuis Internet à certains pays.
- **Dossiers chiffrés** : protègent les données si le NAS ou ses disques sont volés. Après chaque démarrage, un administrateur doit les déverrouiller (Dossiers partagés → Déverrouiller). Gardez la clé de secours en lieu sûr.
- Gardez le système à jour (Mises à jour) : un point de restauration est créé avant chaque mise à jour.

## 10. Accès à distance

Le plus sûr est le **VPN WireGuard** (Accès à distance) : ajoutez un appareil, scannez le QR code avec l'application WireGuard, et vous accédez au NAS comme chez vous. Il faut rediriger le port UDP indiqué sur votre box et, si votre adresse change, activer le **DNS dynamique**. Évitez d'exposer directement l'interface web sur Internet ; si vous le faites, activez le blocage d'adresses et la double authentification.

## 11. Applications et machines virtuelles

L'**App Center** installe des applications prêtes (Jellyfin, Nextcloud, Immich, Home Assistant…) dans Docker ; leurs mises à jour peuvent être programmées. Les **machines virtuelles** (KVM) s'installent en un clic et se créent depuis une ISO (Linux, Windows 11 avec TPM).

## 12. Alimentation

La page **Alimentation** met les disques durs en veille quand ils ne servent pas, choisit la politique du processeur, programme l'arrêt et le redémarrage (par exemple la nuit), et active le réveil par le réseau (Wake-on-LAN). Un arrêt programmé attend la fin d'une sauvegarde ou d'une mise à jour.

## 13. Haute disponibilité

Avec deux NAS ou plus sur le même réseau, l'un (le principal) sert les partages à une **adresse partagée** ; les autres reçoivent une copie des comptes, des partages et des fichiers (en continu ou périodiquement) et prennent le relais en quelques secondes si le principal tombe en panne. Les volumes des secondaires peuvent porter d'autres noms : indiquez la correspondance à l'appairage. Pour redémarrer le principal sans coupure, utilisez « Maintenance ». Après une panne, le principal ne reprend jamais la main seul : rendez-la-lui depuis le secondaire actif, qui recopie d'abord ses modifications.

## 14. Surveiller

- **Tableau de bord** et **Moniteur** : processeur, mémoire, réseau, disques.
- **Journaux** : tout ce qui s'est passé (connexions, tâches, alertes).
- **Rapport mensuel** (Paramètres → Notifications).
- **Prometheus / Grafana** (Paramètres → Supervision) pour un suivi dans la durée.
- **Console multi-NAS** pour voir l'état de plusieurs StrataOS d'un coup d'œil.

## 15. Mises à jour et retour en arrière

Mises à jour → « Mettre à jour ». Avant chaque mise à jour, un **point de restauration** est créé ; si quelque chose ne va plus, « Restaurer » ramène le système et ses programmes à cet état (redémarrage), sans toucher à vos données ni aux réglages de StrataOS.

## 16. En cas de problème

- **L'interface ne répond plus** : vérifiez que le NAS est allumé et sur le réseau ; l'écran de la machine affiche son adresse. Depuis une console, `systemctl status strata` montre l'état du service.
- **Un disque manque au démarrage** : la grappe RAID démarre en mode dégradé et une alerte est envoyée ; remplacez le disque.
- **Un partage n'apparaît plus** : il est peut-être chiffré et verrouillé, gelé par un fichier piège (Antivirus), ou plein (quota).
- **Compte bloqué** : un administrateur le débloque dans Antivirus (rançongiciel) ; une adresse bloquée se débloque dans Blocage d'adresses.
- **Mot de passe administrateur perdu** : connectez-vous en console ou en SSH avec un autre administrateur ; sinon, démarrez sur une clé Debian et réinitialisez le mot de passe du compte Linux, puis changez-le dans l'interface.
