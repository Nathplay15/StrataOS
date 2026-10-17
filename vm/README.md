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
- machines virtuelles en KVM imbriqué (création, console VNC par websocket, ressources, suppression) ;
- démarrage avec un disque RAID débranché, puis remplacement et reconstruction.

Le résultat est écrit dans `vm/last-test.log`. Les identifiants de test sont définis en tête du script.

Options utiles pour tester à la main : `VM_DETACH="2 5" ./vm/vm.sh start` démarre sans les disques 2 et 5 ; `./vm/vm.sh replace-disk 2` (VM arrêtée) remplace le disque 2 par un disque vierge.

## Tester l'ISO d'installation

`vm/iso-test.sh` installe l'ISO dans une VM vierge (disque système de 16 Go, ports 7071 pour l'interface et 2223 pour SSH) et attend que l'interface web réponde :

```bash
make iso
./vm/iso-test.sh dist/strataos-0.2.0-amd64.iso 2 uefi   # 2 disques de données en plus, démarrage UEFI
./vm/iso-test.sh screenshot ecran.png                  # capture de l'écran de la VM
./vm/iso-test.sh key ret                               # envoie une touche (syntaxe sendkey de QEMU)
./vm/iso-test.sh stop
```

Avec des disques en plus, l'installeur demande le disque système (répondre avec `key ret`, le disque de 16 Go est le premier). Le journal du script d'installation reste dans `/var/log/strataos-install.log` de la machine installée.

Machines virtuelles : la virtualisation imbriquée doit être activée sur l'hôte (`cat /sys/module/kvm_amd/parameters/nested` ou `kvm_intel`, doit afficher `1`/`Y`).
