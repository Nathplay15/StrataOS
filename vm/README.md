# VM de test StrataOS

`vm.sh` lance une Debian 13 (image cloud officielle) sous QEMU/KVM, avec **6 disques SATA virtuels de 10 Go** (contrôleur AHCI, donc SMART fonctionne comme sur du vrai matériel) pour tester RAID, ZFS, LVM et Btrfs sans risque pour la machine hôte.

Aucun droit root n'est nécessaire sur l'hôte : seulement un accès à `/dev/kvm` et `qemu-system-x86_64`.

| Commande | Effet |
|---|---|
| `./vm/vm.sh create` | Télécharge et vérifie l'image, crée les disques, premier démarrage (cloud-init) |
| `./vm/vm.sh start` / `stop` | Démarre / arrête proprement la VM |
| `./vm/vm.sh deploy dist/strataos_*.deb` | Copie et installe le paquet dans la VM |
| `./vm/vm.sh ssh [commande]` | Shell (utilisateur `debian`, sudo sans mot de passe) |
| `./vm/vm.sh console` | Suit la console série |
| `./vm/vm.sh reset-disks` | Recrée les 6 disques de données vierges (VM arrêtée) |
| `./vm/vm.sh destroy` | Supprime la VM (l'image de base est conservée) |

## Ports redirigés (sur 127.0.0.1)

| Hôte | VM | Usage |
|---|---|---|
| 7070 | 7070 | Interface StrataOS (HTTP) |
| 7443 | 7443 | Interface StrataOS (HTTPS) |
| 2222 | 22 | SSH |
| 4450 | 445 | SMB (`smb://localhost:4450`) |
| 8096, 8080, 8081, 3001, 9091 | idem | Applications de l'App Center |

Variables : `VM_MEM` (4096 Mio), `VM_CPUS` (4), `VM_DISKS` (6), `VM_DISK_SIZE` (10G), `VM_WEB_PORT`, `VM_SSH_PORT`, `VM_EXTRA_PORTS`.

## Simuler des pannes

- Dans l'UI : **RAID → Marquer en panne** puis **Retirer** et **Ajouter un disque** pour observer la reconstruction.
- Disques vierges : `./vm/vm.sh stop && ./vm/vm.sh reset-disks && ./vm/vm.sh start`.

## Test d'intégration

`make vm-test` recrée une VM neuve, installe le paquet puis lance `./vm/e2e-test.sh` (environ 40 minutes). Le script vérifie, avec des assertions :

- assistant d'installation, authentification, protection CSRF ;
- RAID 5, ext4, Btrfs RAID 1, LVM, refus d'effacer le disque système ;
- groupe, utilisateur, partage SMB/NFS, écriture SMB, ACL sur disque ;
- sécurité du gestionnaire de fichiers (liens symboliques vers `/etc`, noms piégés, HTML) ;
- Docker et installation d'une app ;
- tâches planifiées et rétention des snapshots ;
- redémarrage (session, RAID, volumes, conteneurs, services) ;
- synchronisation cloud (envoi et bidirectionnelle) avec un serveur WebDAV local ;
- WebDAV, FTPS et SFTP : droits des partages, confinement, corbeille, désactivation ;
- Active Directory (contrôleur Samba de test, `vm/test-dc.sh`) et LDAP (OpenLDAP de test, `vm/test-ldap.sh`), lancés dans des conteneurs de la VM ;
- machines virtuelles en KVM imbriqué (création, console VNC par websocket, ressources, suppression) ;
- démarrage avec un disque RAID débranché, puis remplacement et reconstruction.

Le résultat est écrit dans `vm/last-test.log`. Les identifiants de test sont définis en tête du script.

Options utiles pour tester à la main : `VM_DETACH="2 5" ./vm/vm.sh start` démarre sans les disques 2 et 5 ; `./vm/vm.sh replace-disk 2` (VM arrêtée) remplace le disque 2 par un disque vierge.

## Tester l'ISO d'installation

`vm/iso-test.sh` installe l'ISO dans une VM vierge (disque système de 16 Go, ports 7071 pour l'interface et 2223 pour SSH) et attend que l'interface web réponde :

```bash
make iso
./vm/iso-test.sh dist/strataos-0.3.0-amd64.iso 2 uefi   # 2 disques de données en plus, démarrage UEFI
./vm/iso-test.sh dist/strataos-0.3.0-arm64.iso 1 arm64  # ISO arm64 dans une machine ARM émulée (lent)
./vm/iso-test.sh screenshot ecran.png                  # capture de l'écran de la VM
./vm/iso-test.sh key ret                               # envoie une touche (syntaxe sendkey de QEMU)
./vm/iso-test.sh stop
```

Avec des disques en plus, l'installeur demande le disque système (répondre avec `key ret`, le disque de 16 Go est le premier). Le journal du script d'installation reste dans `/var/log/strataos-install.log` de la machine installée.

Machines virtuelles : la virtualisation imbriquée doit être activée sur l'hôte (`cat /sys/module/kvm_amd/parameters/nested` ou `kvm_intel`, doit afficher `1`/`Y`).

## Tester la haute disponibilité

`make ha-test` recrée deux VM reliées par un réseau local commun (multicast QEMU, sans droits root) : A (principal, 10.99.0.1) et B (secondaire, 10.99.0.2), adresse partagée 10.99.0.100. Le script vérifie l'appairage, la réplication (données, comptes avec les mêmes UID), la bascule quand A est arrêté brutalement (SMB servi par B à l'adresse partagée, connexion d'un utilisateur répliqué), le retour de A en attente, le retour au principal avec les modifications faites pendant la panne, puis le désappairage.

Pour lancer une deuxième VM à la main : `VM_STATE=$PWD/vm/state-b VM_HOSTNAME=strata-b VM_WEB_PORT=7080 VM_SSH_PORT=2232 VM_HTTPS_PORT=7453 VM_SMB_PORT=4460 SEED_PORT=8729 VM_EXTRA_PORTS= VM_LAN=230.0.0.1:12345 ./vm/vm.sh create` (et `VM_LAN=230.0.0.1:12345` sur la première).

## Tester le passage d'une version à l'autre

`make upgrade-test` construit la version précédente depuis git (dernière étiquette, ou `OLD_REF=<ref>`), l'installe dans une VM neuve (ports 7100 et 2252), y crée des volumes, un partage avec quota, un utilisateur, un fichier, une tâche de snapshots, une sauvegarde et des réglages, puis installe le paquet courant par-dessus : les données, comptes, tâches et réglages doivent être conservés, les tâches doivent encore fonctionner, aucune erreur ne doit apparaître, y compris après un redémarrage.

## Tester le paquet arm64

`make arm64-test` construit le paquet arm64 et l'installe dans une VM ARM émulée (`VM_ARCH=arm64`, sans KVM : environ une heure ; ports 7090 et 2242) : assistant, volume Btrfs, partage SMB, envoi de fichier, dossier chiffré, blocage d'adresses, métriques. Il faut `qemu-system-aarch64` et le micrologiciel AAVMF (paquets `qemu-system-arm` et `qemu-efi-aarch64`, installés ou extraits dans `~/.local/opt/qemu-arm`).
