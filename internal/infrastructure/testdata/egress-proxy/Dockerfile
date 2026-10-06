FROM golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 AS build
WORKDIR /build
COPY denied_hosts.go denied_hosts_test.go ./
RUN go test denied_hosts.go denied_hosts_test.go && CGO_ENABLED=0 go build -trimpath -o /build/denied-hosts denied_hosts.go
RUN curl --fail --location --max-time 60 --output tinyproxy.tar.gz https://github.com/tinyproxy/tinyproxy/releases/download/1.11.3/tinyproxy-1.11.3.tar.gz \
    && echo '9bcf46db1a2375ff3e3d27a41982f1efec4706cce8899ff9f33323a8218f7592  tinyproxy.tar.gz' | sha256sum -c - \
    && tar -xzf tinyproxy.tar.gz \
    && cd tinyproxy-1.11.3 \
    && ./configure --disable-transparent --disable-reverse --disable-upstream \
    && make -j2 \
    && make install

FROM debian:trixie-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
COPY --from=build /usr/local/bin/tinyproxy /usr/local/bin/tinyproxy
COPY --from=build /build/denied-hosts /usr/local/bin/denied-hosts
COPY --from=build /usr/local/share/doc/tinyproxy /usr/local/share/doc/tinyproxy
COPY --from=build /build/tinyproxy-1.11.3/COPYING /usr/local/share/doc/tinyproxy/COPYING
USER 65532:65532
CMD ["/usr/local/bin/tinyproxy", "-d", "-c", "/run/proxy/tinyproxy.conf"]
