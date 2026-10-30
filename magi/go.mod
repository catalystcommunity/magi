module github.com/catalystcommunity/magi/magi

go 1.26.0

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
	github.com/containerd/stargz-snapshotter/estargz v0.14.3 // indirect
	github.com/docker/cli v27.1.1+incompatible // indirect
	github.com/docker/distribution v2.8.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.7.0 // indirect
	github.com/klauspost/compress v1.16.5 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.0-rc3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.9.1 // indirect
	github.com/vbatts/tar-split v0.11.3 // indirect
	golang.org/x/sync v0.18.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
)

require (
	github.com/catalystcommunity/magi/primitives/release/goapi v0.0.0
	github.com/google/go-containerregistry v0.20.2
	golang.org/x/term v0.37.0
)

replace github.com/catalystcommunity/magi/primitives/release/goapi => ../primitives/release/goapi
