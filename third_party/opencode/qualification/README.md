# Qualification-only OpenCode patches

Upstream: https://github.com/anomalyco/opencode
Base commit: `16747470f976aca3d362ad730bcd3fe82ecc2c9a` (1.18.29).
MIT license is retained in `../LICENSE`.

`r29-r31.patch` is the exact combined local diff. R29 changes persisted output
format schemas from nominal classes to equivalent structural schemas so complete
history readback retains schema, value, model and usage. R31 changes structured
output tool choice to `auto` only for provider `engorch-opencode-zen` and model
`muse-spark-1.3-contributor-free`. Every other pair remains `required`.

EngOrch still requires exactly one valid structured capture. No free-text recovery,
new retry, model fallback, schema relaxation or usage-accounting change is granted.

Local diagnostic build used Bun 1.3.14 and:
`bun run script/build.ts --single --skip-embed-web-ui --skip-install`
with version `1.18.29-r31-qualification`. The web UI was not embedded. This is not
a byte-identical stock build. No compiled binary is distributed here.

Combined patch SHA-256:
`5c2c7fedf449b1d8e3f376d5dbcb6cadc9555a5d67dd60e4100f20cefaead75a`

Diagnostic binary SHA-256:
`8d46d1ba058b597f739a579ffd4b68f2fa06e9627c1f4f801405fde3db3c61b2`

These patches are not automatically approved for production or v0.0.1 promotion.
