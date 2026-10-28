# Ceph without a cluster (the full test with three NAS is vm/ceph-test.sh):
# API answers before the component is installed, validation of the cluster
# of NAS and of external clusters, peer API authentication, rights.
section "Ceph and cluster of NAS (single NAS)"

check "Ceph not installed" "$(c GET /api/ceph | py 'print(d["installed"], d["local"])')" "False None"
check "no cluster" "$(c GET /api/cluster | py 'print(d["cluster"], len(d["members"]), len(d["networks"]) > 0)')" "None 0 True"
check "Ceph needs a cluster of NAS" "$(c POST /api/ceph/bootstrap -d '{}')" "create the cluster of NAS first"
check "invalid network refused" "$(c POST /api/cluster -d '{"name":"Lab","publicNet":"10.0.2.15/24"}')" "invalid network"
check "network without an address refused" "$(c POST /api/cluster -d '{"name":"Lab","publicNet":"198.51.100.0/24"}')" "no address in the network"
NET=$(c GET /api/cluster | py 'print(d["networks"][0])')
check "cluster created" "$(c POST /api/cluster -d "{\"name\":\"E2E lab\",\"publicNet\":\"$NET\"}" | py 'print(d["name"], len(d["members"]), d["token"] if "token" in d else "")')" "E2E lab 1"
check "token never sent back" "$(c GET /api/cluster | py 'print(repr(d["cluster"].get("token") or None))')" "None"
check "already in a cluster" "$(c POST /api/cluster/prepare)" "already in a cluster"
check "peer API without token refused" "$(vm 'curl -sk -X POST https://127.0.0.1:7443/api/cluster/peer/info')" "unknown NAS"
check "peer join with a wrong code refused" "$(vm "curl -sk -X POST -H 'Content-Type: application/json' -d '{\"code\":\"AAAAA-BBBBB\"}' https://127.0.0.1:7443/api/cluster/peer/join")" "wrong or expired"
check "invalid member address refused" "$(c POST /api/cluster/members -d '{"host":"https://x/","code":"A"}')" "invalid address"
check "Ceph needs its component" "$(c POST /api/ceph/bootstrap -d '{}')" "install the Ceph component first"
check "user cannot read the cluster" "$(u GET /api/cluster)" "error"
check "cluster left" "$(c POST /api/cluster/leave) $(c GET /api/cluster | py 'print(d["cluster"])')" "None"

check "external: bad key refused" "$(c POST /api/ceph/external/test -d '{"mons":"10.0.2.2","user":"client.nas","key":"secret"}')" "invalid Ceph key"
check "external: bad monitor refused" "$(c POST /api/ceph/external/test -d '{"mons":"10.0.2.2;rm","user":"nas","key":"AQBSdFRiAAAAABAAhYqOzKwRFT1CRGlWbTOKEQ=="}')" "invalid monitor address"
check "Ceph volume needs the component" "$(c POST /api/ceph/volumes -d '{"name":"cephx","source":"local","kind":"cephfs","fs":"cephfs"}')" "install the Ceph component"
check "no Ceph storage" "$(c GET /api/ceph/storage | py 'print(d["sources"], d["volumes"], d["vmPools"])') $(c GET /api/ceph/vm-pools)" "[] [] [] []"
check "Ceph component listed" "$(c GET /api/system/components | py 'print(d["ceph"])')" "False"
