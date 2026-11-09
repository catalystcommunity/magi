module github.com/catalystcommunity/magi/magi

go 1.26.4

replace (
	github.com/catalystcommunity/magi/lib/authn => ../lib/authn
	github.com/catalystcommunity/magi/lib/rpc => ../lib/rpc
	github.com/catalystcommunity/magi/primitives/artifacts => ../primitives/artifacts
	github.com/catalystcommunity/magi/primitives/conversation => ../primitives/conversation
	github.com/catalystcommunity/magi/primitives/execution => ../primitives/execution
	github.com/catalystcommunity/magi/primitives/identity => ../primitives/identity
	github.com/catalystcommunity/magi/primitives/knowledge => ../primitives/knowledge
	github.com/catalystcommunity/magi/primitives/source => ../primitives/source
	github.com/catalystcommunity/magi/primitives/speech => ../primitives/speech
	github.com/catalystcommunity/magi/primitives/workflow => ../primitives/workflow
)

require (
	github.com/catalystcommunity/magi/lib/authn v0.0.0
	github.com/catalystcommunity/magi/lib/rpc v0.0.0
	github.com/catalystcommunity/magi/primitives/artifacts v0.0.0
	github.com/catalystcommunity/magi/primitives/conversation v0.0.0
	github.com/catalystcommunity/magi/primitives/execution v0.0.0
	github.com/catalystcommunity/magi/primitives/identity v0.0.0
	github.com/catalystcommunity/magi/primitives/knowledge v0.0.0
	github.com/catalystcommunity/magi/primitives/source v0.0.0
	github.com/catalystcommunity/magi/primitives/speech v0.0.0
	github.com/catalystcommunity/magi/primitives/workflow v0.0.0
)

require (
	github.com/CatalystCommunity/corndogs/clients/corndogs v0.0.0-20260930033320-f1b6ec6086dd // indirect
	github.com/catalystcommunity/catalystcal/clients/go v0.3.0 // indirect
	github.com/catalystcommunity/longhouse/clients/go v0.0.0-20261108205303-831c2556920a // indirect
	github.com/containerd/stargz-snapshotter/estargz v0.14.3 // indirect
	github.com/docker/cli v27.1.1+incompatible // indirect
	github.com/docker/distribution v2.8.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.7.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/nftables v0.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.16.5 // indirect
	github.com/mdlayher/netlink v1.7.3-0.20250113171957-fbb4dce95f42 // indirect
	github.com/mdlayher/socket v0.8.0 // indirect
	github.com/mdlayher/vsock v1.4.0 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.0-rc3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.9.1 // indirect
	github.com/vbatts/tar-split v0.11.3 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

require (
	github.com/catalystcommunity/magi/lib/revconn v0.0.0
	github.com/catalystcommunity/magi/primitives/release/goapi v0.0.0
	github.com/google/go-containerregistry v0.20.2
	github.com/jackc/pgx/v5 v5.11.0
	golang.org/x/net v0.62.0
	golang.org/x/term v0.47.0
)

replace github.com/catalystcommunity/magi/primitives/release/goapi => ../primitives/release/goapi

replace github.com/catalystcommunity/magi/lib/revconn => ../lib/revconn
