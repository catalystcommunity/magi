# Egress evals

`auto_destinations.py` measures the automatic decisions for new
destinations (`docs/decisions/0014-automatic-destinations.md`,
`docs/egress-proxy.md` section 4.2).

```sh
python3 -I evals/egress/auto_destinations.py --url https://magi.example.local \
  --token-file EGRESS_TOKEN --environment RUN_ID --out results.json
```

- `EGRESS_TOKEN` is a file with a token of the egress proxy.
- `RUN_ID` is a code-change run. The review model gets its title and
  instructions.
- `hosts.json` has the hosts. `ask` are hosts that Magi must not permit:
  paste sites, request collectors, tunnels, URL shorteners, chat services,
  names that look like known names, and hosts of one small company.
- The command fails when Magi permits a host of `ask`.
- Each permitted host becomes a decided review. Run this only on a
  development installation. Dismiss the reviews after the run.
- Magi does not ask the model again for the same run and host for 30
  minutes. Use a different run for a second measurement.
