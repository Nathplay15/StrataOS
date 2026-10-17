# StrataOS build entry points.
export PATH := $(HOME)/.local/bin:$(PATH)
VERSION := $(shell cat VERSION)
ARCH ?= amd64

.PHONY: all frontend backend build deb repo dev clean test vm-test vm-create vm-start vm-stop vm-ssh vm-deploy vm-destroy

all: deb

frontend:
	cd frontend && npm ci --no-audit --no-fund && npm run build
	touch backend/web/dist/.gitkeep  # vite empties the folder; keep it for go:embed in fresh clones

backend:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o ../build/strata-linux-$(ARCH) ./cmd/strata

build: frontend backend

deb: build
	ARCH=$(ARCH) REPO_URL=$(REPO_URL) ./packaging/build-deb.sh

# Signed APT repository in dist/apt (serve it, and build with REPO_URL=<its URL>).
repo:
	./packaging/make-repo.sh key
	$(MAKE) deb REPO_URL=$(REPO_URL)
	./packaging/make-repo.sh

# Frontend dev server proxying the API to the test VM (http://localhost:7070).
dev:
	cd frontend && npm run dev

vet:
	cd backend && go vet ./... && cd ../frontend && npx tsc -b

# Unit tests (no root, no VM needed).
test:
	cd backend && go test ./...

# Full end-to-end test on a brand new VM with 8 disks (about 20 minutes): storage, ZFS, shares,
# security, notifications, Docker, scheduled tasks, reboot, missing disks, RAID and ZFS rebuild.
vm-test: deb
	./vm/vm.sh destroy
	VM_DISKS=8 ./vm/vm.sh create
	./vm/vm.sh deploy dist/strataos_$(VERSION)_$(ARCH).deb
	./vm/e2e-test.sh

clean:
	rm -rf build dist backend/web/dist/assets

# ---- Test VM (see vm/README.md) ----
vm-create:
	./vm/vm.sh create
vm-start:
	./vm/vm.sh start
vm-stop:
	./vm/vm.sh stop
vm-ssh:
	./vm/vm.sh ssh
vm-deploy: deb
	./vm/vm.sh deploy dist/strataos_$(VERSION)_$(ARCH).deb
vm-destroy:
	./vm/vm.sh destroy
