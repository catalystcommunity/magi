package conversationschema

import _ "embed"

//go:embed go/conversations.csil-schema.cbor
var Descriptor []byte

const Digest = "33c0b63b86f422619926fe57fda4f0473a5b39f1fa4248174d63c1441786a150"
