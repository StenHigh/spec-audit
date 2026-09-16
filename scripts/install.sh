#!/bin/sh
# Установка spec-audit из релиза GitHub: скачивает бинарник своей платформы, сверяет sha256 с
# release-manifest.json того же релиза и кладёт его в $SPEC_AUDIT_INSTALL_DIR (по умолчанию ~/.local/bin).
# Без sudo и правки профилей оболочки. Первая установка доверяет HTTPS до GitHub; дальнейшие
# обновления проверяет `spec-audit update` подписью Ed25519 (tool-spec §18).
set -eu

base="${SPEC_AUDIT_RELEASE_BASE:-https://github.com/StenHigh/spec-audit/releases/latest/download}"
dest="${SPEC_AUDIT_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	printf 'spec-audit install: %s\n' "$1" >&2
	exit 1
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os/$arch" in
darwin/arm64) ;;
linux/x86_64) arch=amd64 ;;
*) fail "платформа $os/$arch не поддерживается (только darwin/arm64 и linux/amd64)" ;;
esac
asset="spec-audit-$os-$arch"

command -v curl >/dev/null 2>&1 || fail "нужен curl"
if command -v shasum >/dev/null 2>&1; then
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
else
	fail "нужен shasum или sha256sum"
fi

tmp=$(mktemp -d)
staged="$dest/.spec-audit.staged.$$"
cleanup() { rm -rf "$tmp" "$staged"; }
trap cleanup 0 HUP INT TERM

curl --fail --location --silent --show-error "$base/release-manifest.json" -o "$tmp/manifest.json" || fail "манифест релиза недоступен"
# sha256 нашего ассета без jq: убрать пробелы, разрезать по объектам, взять объект с нашим file.
expected=$(tr -d ' \n\r\t' <"$tmp/manifest.json" | tr '}' '\n' | grep "\"file\":\"$asset\"" | sed -n 's/.*"sha256":"\([0-9a-f]*\)".*/\1/p' | head -n 1)
[ "${#expected}" -eq 64 ] || fail "в манифесте нет sha256 для $asset"
case "$expected" in *[!0-9a-f]*) fail "sha256 в манифесте повреждён" ;; esac

curl --fail --location --silent --show-error "$base/$asset" -o "$tmp/$asset" || fail "ассет $asset недоступен"
actual=$(sha "$tmp/$asset")
[ "$actual" = "$expected" ] || fail "sha256 скачанного файла не совпадает с манифестом; установка отменена"

mkdir -p "$dest"
cp "$tmp/$asset" "$staged"
chmod 755 "$staged"
mv -f "$staged" "$dest/spec-audit"

printf '%s\n' "$dest/spec-audit"
case ":$PATH:" in
*":$dest:"*) ;;
*) printf 'Добавьте %s в PATH; профиль оболочки не изменялся.\n' "$dest" ;;
esac
printf 'Далее: spec-audit skill install --dir <проект> --host codex|claude|both\n'
