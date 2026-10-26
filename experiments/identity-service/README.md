# Authenticated approval experiment

This is the W02 service experiment. It uses two real LinkKeys servers and
separate approval stores. The [report](../../docs/identity-service.md) defines
the tested boundary and the limits before deployment.

## Run

Requirements:

- Node.js 26 and Go 1.27.
- Docker access. The tests create an internal network and three containers.
- The cached LinkKeys 0.16.2 image with digest
  `sha256:b537c0fa9d85ec035c7bd61285ebb0a6980795cce81cf8a3afa640218b64b567`.
- The cached Node image with digest
  `sha256:0353e48e0e8a993db87b720c242f54b207059d1bcc0106534896e8a11054c837`.
- The sibling LinkKeys checkout at
  `03fcbd8e700715fb686bce2016136ecd8071f307`. Its regular-RP TypeScript build
  dependencies must be installed. The experiment does not change that checkout.
- CSILgen 0.2.9 and the generator files checked by `generate.mjs`.

From this directory:

```sh
node run.mjs 3
```

The runner builds the SDK in `.sdk/`, checks generated clients, runs the tests,
and runs Go vet. It records test events, source hashes, versions, and results
under `results/`. `.sdk/` is build output, not source to edit.

To update generated output after a schema change:

```sh
node generate.mjs
node generate.mjs --check
```

The fixture has no published container ports. It uses a Docker internal
network, private DNS, synthetic credentials, and separate LinkKeys databases.
The browser driver accepts the isolated servers' HTTPS certificates. Backend
RPC uses the actual LinkKeys TLS pins. Private peer access and larger TLS
handshake limits apply only to these test servers. API-key calls are paced.

Cleanup checks each container's label before removal. It removes only this
run's containers and network. Their synthetic LinkKeys databases are not
recoverable. Private temporary Magi databases and the recorded results remain.
Neither catalystsquad.local nor partner.local is used.

## Source map

| File | Purpose |
| --- | --- |
| `schema/approval.csil` | Typed records and prototype operations |
| `approval.mjs` | Login state, approval transaction, publication, and receiver rules |
| `http.mjs` | Bounded loopback CSIL gateway and browser cookie checks |
| `harness.mjs` | Isolated LinkKeys servers, accounts, and key enrollment |
| `dns.mjs` | Private test DNS without forwarding |
| `integration.test.mjs` | Authentication, federation, policy, and process recovery |
| `codec.test.mjs` | Go/TypeScript byte checks and invalid input tests |
| `crash-child.mjs` | Separate process for commit failures and concurrent retries |

## Next Actions

Extract the tested interfaces for W03. Add the production store, policy
management, audit output, and HTTPS configuration before deployment. Connect
publication to W04 and operation receipts to W05.
