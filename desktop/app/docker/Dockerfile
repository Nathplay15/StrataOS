# Build environment of the desktop app for Linux (Rust, Tauri's system
# libraries, Xvfb for screenshots). Node and Go come from the host or are
# installed by the caller; see docs/DEVELOPMENT.md, "Desktop app".
FROM rust:1-trixie
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      libwebkit2gtk-4.1-dev libayatana-appindicator3-dev librsvg2-dev libxdo-dev libssl-dev \
      build-essential file patchelf xdg-utils desktop-file-utils \
      xvfb xauth x11-apps imagemagick dbus-x11 fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/*
RUN rustup component add rustfmt clippy && chmod -R a+rX /usr/local/rustup /usr/local/cargo
