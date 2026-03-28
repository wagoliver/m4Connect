#!/bin/bash
# Compila e empacota M4Server.pkg
# Execute no Mac Mini: bash pkg/build_pkg.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BUILD_DIR="$SCRIPT_DIR/build"
PAYLOAD_DIR="$BUILD_DIR/payload/usr/local/m4server"
SCRIPTS_DIR="$BUILD_DIR/scripts"
OUTPUT="$SCRIPT_DIR/M4Server.pkg"

VERSION="1.1.0"
HASH=$(git -C "$PROJECT_DIR" rev-parse --short HEAD 2>/dev/null || echo "unknown")

echo "==> Compilando binário Go... (v${VERSION} · ${HASH})"
cd "$PROJECT_DIR"
go build -ldflags "-X main.Version=${VERSION} -X main.BuildHash=${HASH}" -o "$PROJECT_DIR/m4server" .
echo "    ✓ m4server v${VERSION} (${HASH}) compilado"

echo "==> Preparando payload..."
rm -rf "$BUILD_DIR"
mkdir -p "$PAYLOAD_DIR" "$SCRIPTS_DIR"

cp "$PROJECT_DIR/m4server" "$PAYLOAD_DIR/"
cp -r "$PROJECT_DIR/pkg"   "$PAYLOAD_DIR/"

echo "==> Preparando scripts..."
cp "$SCRIPT_DIR/postinstall" "$SCRIPTS_DIR/postinstall"
chmod +x "$SCRIPTS_DIR/postinstall"

echo "==> Criando M4Server.pkg..."
pkgbuild \
  --root "$BUILD_DIR/payload" \
  --scripts "$SCRIPTS_DIR" \
  --identifier "com.m4server.daemon" \
  --version "1.0.0" \
  --install-location "/" \
  "$OUTPUT"

rm -f "$PROJECT_DIR/m4server"

echo ""
echo "✓ Pacote criado: $OUTPUT"
echo "  Instale com:  sudo installer -pkg $OUTPUT -target /"
