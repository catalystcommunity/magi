package speechschema

import _ "embed"

//go:embed go/speech.csil-schema.cbor
var Descriptor []byte

const Digest = "2f6f49f2338f0ffc7acdbbf7dcb957db74371da47d660e5a9f0835b49788bd7c"
