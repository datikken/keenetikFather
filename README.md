# CRSF2: маршрутизация LAN-пар через AmneziaWG

Docker-шлюз на VDS соединяет пары сетей Radio и Module через AmneziaWG. Новые пары можно добавлять командой. ESP32 остаются обычными LAN-устройствами: VPN-клиент работает на роутере или отдельном Linux-шлюзе.

Настройки CRSF2 из [технического задания](./inintial/tech_task.pdf) менять не нужно: Module остаётся мастером с постоянным IP `192.168.55.11`, Radio подключается к этому адресу, обмен идёт по UDP `7777`. Между LAN не используется SNAT/masquerade. На VDS открыт только внешний UDP-порт VPN; публиковать UDP `7777` в интернет не требуется.

## Адреса первой пары

| Узел | VPN-адрес | Локальная сеть |
|---|---:|---|
| VDS / AmneziaWG | `10.77.0.1/24` | — |
| Шлюз Radio | `10.77.0.2/32` | `192.168.66.0/24` |
| Шлюз Module | `10.77.0.3/32` | `192.168.55.0/24` |

Пример предполагает Linux VDS и Docker Engine с поддержкой `network_mode: host` и `/dev/net/tun`.

## 1. Подготовьте VDS

Установите Docker Engine, Docker Compose plugin и Go 1.22 или новее. В облачном firewall и на хосте разрешите входящий **UDP `51820`**. Не открывайте `7777` в интернет.

Включите IPv4 forwarding на хосте:

```sh
sudo install -m 0644 /dev/stdin /etc/sysctl.d/99-crsf2-routing.conf <<'EOF'
net.ipv4.ip_forward=1
EOF
sudo sysctl --system
```

Для временного включения можно выполнить `sudo sysctl -w net.ipv4.ip_forward=1`. Контейнер откажется запускаться, если forwarding выключен.

## 2. Запустите шлюз

Перед запуском создайте рабочие конфиги из шаблонов и замените все `CHANGE_ME_...` отдельными ключами сервера, Radio и Module:

```sh
docker compose build
cp config/server.conf.example config/server.conf
cp config/radio-peer.conf.example config/radio-peer.conf
cp config/module-peer.conf.example config/module-peer.conf
docker run --rm --entrypoint awg crsf2-amneziawg:local genkey
```

Сгенерируйте три независимых приватных ключа командой `genkey`, вычислите для каждого публичный через `printf '%s\n' 'ПРИВАТНЫЙ_КЛЮЧ' | docker run --rm -i --entrypoint awg crsf2-amneziawg:local pubkey` и внесите значения в соответствующие шаблоны. Укажите публичный IP или DNS VDS в `Endpoint` обоих клиентских профилей. Параметры AmneziaWG должны совпадать у сервера и клиентов.

Файлы с приватными ключами исключены из Git и Docker build context; ограничьте к ним доступ (`chmod 600 config/server.conf config/radio-peer.conf config/module-peer.conf`) и храните их в безопасности.

```sh
docker compose config
docker compose up -d --build
docker compose ps
docker compose logs --tail=100 amneziawg
```

Контейнер создаёт userspace-интерфейс `awg0` и устанавливает маршруты для всех `AllowedIPs` из peer-записей. Используется host networking, поэтому секция `ports` отсутствует.

Проверка на VDS:

```sh
sudo docker exec crsf2-amneziawg awg show awg0
ip route get 192.168.66.73
ip route get 192.168.55.11
```

После подключения клиентов в `awg show` должны появиться handshake и счётчики передачи.

## 3. Добавляйте пары Radio + Module

После настройки первой пары и запуска шлюза из каталога проекта на VDS выполните одну команду, указав уникальное имя пары, LAN Radio, LAN Module и публичный endpoint VDS. `site-02` ниже — условное имя второй площадки: замените его на своё, например `warehouse`, `office` или `branch-02`.

```sh
go run ./cmd/add-pair warehouse 192.168.67.0/24 192.168.56.0/24 vpn.example.com:51820
```

Имя должно быть уникальным и содержать только строчные латинские буквы, цифры, дефис или подчёркивание. По имени создаётся каталог с профилями — например, для `warehouse`:

```text
config/pairs/warehouse/radio.conf
config/pairs/warehouse/module.conf
```

Команда проверит подсети и конфигурацию сервера, автоматически выделит два свободных VPN-адреса и сгенерирует отдельные ключи для Radio и Module.

Она добавит peer-записи в `config/server.conf`. Если шлюз запущен, команда перезапустит его и дождётся `healthy`; если выключен — сообщит выполнить `docker compose up -d`. Нужны Go 1.22+, Docker Compose и собранный образ (`docker compose build`).

Клиентские профили содержат приватные ключи, имеют права только для владельца и исключены из Git. Передавайте каждый профиль только соответствующему шлюзу. Подсети новых LAN не должны пересекаться друг с другом, с VPN-сетью или с уже добавленными peer-сетями. Каждая пара по умолчанию маршрутизирует только свою Radio LAN к своей Module LAN.

## 4. Настройте роутеры/шлюзы

Импортируйте профиль соответствующей стороны как **AmneziaWG**. Обычный WireGuard-клиент может не поддерживать параметры AmneziaWG. Для первой пары в клиентских файлах задайте публичный IP/DNS VDS в `Endpoint`; профили, создаваемые командой добавления пар, получают endpoint из аргумента команды.

- Radio peer: удалённая LAN — сеть Module.
- Module peer: удалённая LAN — сеть Radio.
- Включите автоподключение. `PersistentKeepalive = 25` помогает шлюзам за NAT.

Если VPN-клиент установлен на отдельном устройстве в LAN, добавьте на основном роутере статический маршрут к удалённой LAN через LAN-адрес этого устройства. На роутерах разрешите forwarding между LAN и VPN. Не включайте SNAT/masquerade для трафика между парами.

Команда добавления peer не меняет правила firewall хоста. На VDS firewall должен разрешать forwarding между LAN каждой пары в обоих направлениях; добавьте правила для каждой новой пары в используемый постоянный firewall. Например, для первой пары в iptables:

```sh
sudo iptables -I FORWARD 1 -i awg0 -o awg0 -s 192.168.66.0/24 -d 192.168.55.0/24 -j ACCEPT
sudo iptables -I FORWARD 1 -i awg0 -o awg0 -s 192.168.55.0/24 -d 192.168.66.0/24 -j ACCEPT
```

Адаптируйте правила для nftables или другого firewall и сохраняйте их штатным для дистрибутива способом. Не добавляйте для этих LAN правила NAT. Если публичные адреса клиентов постоянны, ограничьте ими входящий UDP `51820`.

## 5. Проверьте CRSF2

1. Проверьте handshake командой `awg show awg0`.
2. С компьютера в сети Radio проверьте Module: `ping -n 20 192.168.55.11` в Windows или `ping -c 20 192.168.55.11` в Linux.
3. Проверьте обратный маршрут, firewall и отсутствие NAT.
4. Для проверки CRSF2 наблюдайте Serial Monitor обеих плат на `115200` бод: установленная сессия, `NET ONLINE`, рост сетевых `tx`/`rx` и чтение параметров Nomad. Целевой RTT — менее `250 мс`.

Ping проверяет ICMP, а не UDP или CRSF2. `Test-NetConnection -Port 7777` проверяет TCP и не подходит для проверки UDP-сессии.

## Эксплуатация

```sh
docker compose logs -f amneziawg
docker compose restart amneziawg
docker compose down
```

Подробнее: [requirements.md](./inintial/requirements.md) и [tech_task.pdf](./inintial/tech_task.pdf). Сетевые значения примеров следует сверить с фактической адресацией.
