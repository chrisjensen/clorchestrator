---
description: Identify and run necessary tests to provide full coverage
model: sonnet
effort: medium
---

Identify the minimum set of tests necessary to be confident that the changes in this branch work appropriately and do not break any features.

Avoid overtesting as every test but the first invokes LLM token costs, only run them if other tests are unable to validate the changes.

Keep in mind that npm test has already been run. If that provides sufficient coverage for the changes, it's acceptable to do no further testing.

- ./bin/dev resources/config/test-convert-viajson.toml
- npm run test:blueprint:spv1-create
- npm run test:blueprint:spv1-extract
- npm run test:blueprint:spv2-update
- npm run test:blueprint:spv2-extract

Report any failures to the user for their investigation