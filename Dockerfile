FROM golang:1.25.0-bookworm AS build

ARG AWG_GO_COMMIT=b5928efb6ca19f0153958460c3d141f04abc5c2e
ARG AWG_TOOLS_COMMIT=ee0f0a9aa34ff0a0da4b3433b9512781cfe02843

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates git make \
    && rm -rf /var/lib/apt/lists/*

RUN git init /src/amneziawg-go \
    && git -C /src/amneziawg-go remote add origin https://github.com/amnezia-vpn/amneziawg-go.git \
    && git -C /src/amneziawg-go fetch --depth 1 origin "${AWG_GO_COMMIT}" \
    && git -C /src/amneziawg-go checkout --detach FETCH_HEAD \
    && make -C /src/amneziawg-go

RUN git init /src/amneziawg-tools \
    && git -C /src/amneziawg-tools remote add origin https://github.com/amnezia-vpn/amneziawg-tools.git \
    && git -C /src/amneziawg-tools fetch --depth 1 origin "${AWG_TOOLS_COMMIT}" \
    && git -C /src/amneziawg-tools checkout --detach FETCH_HEAD \
    && make -C /src/amneziawg-tools/src \
        WITH_BASHCOMPLETION=no \
        WITH_WGQUICK=yes \
        WITH_SYSTEMDUNITS=no \
    && make -C /src/amneziawg-tools/src install \
        DESTDIR=/out \
        PREFIX=/usr \
        WITH_BASHCOMPLETION=no \
        WITH_WGQUICK=yes \
        WITH_SYSTEMDUNITS=no

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates iproute2 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /src/amneziawg-go/amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=build /out/usr/bin/awg /usr/bin/awg
COPY --from=build /out/usr/bin/awg-quick /usr/bin/awg-quick
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh

RUN chmod 0755 /usr/local/bin/entrypoint.sh

ENV AWG_INTERFACE=awg0 \
    AWG_CONFIG_FILE=/etc/amnezia/amneziawg/awg0.conf \
    AWG_ADDRESS=10.77.0.1/24 \
    RADIO_LAN=192.168.66.0/24 \
    MODULE_LAN=192.168.55.0/24

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
