#!/bin/sh
# Install only the two missing API host mappings; preserve the current OpenClash mode.
set -eu
current="$(uci -q get 'dhcp.@dnsmasq[0].address' || true)"
for domain in api.yeutech.cn llm-api.yeutech.cn; do
    for item in $current; do
        case "$item" in
            "/$domain/192.168.100.234") ;;
            "/$domain/"*) echo "Conflicting mapping for $domain; inspect before changing" >&2; exit 1 ;;
        esac
    done
done
if [ "${1:-}" != --apply ]; then
    echo 'Plan: map api.yeutech.cn and llm-api.yeutech.cn to 192.168.100.234; reload dnsmasq only'
    exit 0
fi
backup="/etc/config/dhcp.before-api-lan-$(date +%Y%m%d-%H%M%S)"
cp -p /etc/config/dhcp "$backup"
for domain in api.yeutech.cn llm-api.yeutech.cn; do
    case " $current " in
        *" /$domain/192.168.100.234 "*) ;;
        *) uci add_list "dhcp.@dnsmasq[0].address=/$domain/192.168.100.234" ;;
    esac
done
uci commit dhcp
/etc/init.d/dnsmasq reload
echo "Backup: $backup"
echo 'OpenClash mode unchanged; existing TCP connections unchanged'
