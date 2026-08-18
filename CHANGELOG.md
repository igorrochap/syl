# CHANGELOG

<!-- version list -->

## v1.7.0 (2026-08-18)

### Features

- **config**: Add MCP configuration support for roles
  ([#43](https://github.com/igorrochap/syl/pull/43),
  [`49928df`](https://github.com/igorrochap/syl/commit/49928dfdb0a19f2fd8cafc3143cf29b7182e1ad9))


## v1.6.0 (2026-08-18)

### Documentation

- **code-review**: Clarify coordinator responsibilities
  ([`b4b2633`](https://github.com/igorrochap/syl/commit/b4b2633d0c54930a265952e1542bc230b85f9b91))

### Features

- **review**: Precompute authoritative diff for reviews
  ([#42](https://github.com/igorrochap/syl/pull/42),
  [`36374db`](https://github.com/igorrochap/syl/commit/36374db9a04819e060588544951b9f452d85f4f8))

### Refactoring

- **orchestration**: Optimize review verdict handling
  ([`8ad2fa7`](https://github.com/igorrochap/syl/commit/8ad2fa7853088aefa3868281db845a55d000e620))


## v1.5.0 (2026-08-18)

### Chores

- **config**: Initialize syl project configuration
  ([#35](https://github.com/igorrochap/syl/pull/35),
  [`659e324`](https://github.com/igorrochap/syl/commit/659e3241232fbbb876e33599d2000088df9144cc))

### Features

- **orchestration**: Improve harness question interface
  ([#35](https://github.com/igorrochap/syl/pull/35),
  [`659e324`](https://github.com/igorrochap/syl/commit/659e3241232fbbb876e33599d2000088df9144cc))


## v1.4.1 (2026-08-18)

### Features

- **cli**: Add activity spinner for quiet mode
  ([`bf070ee`](https://github.com/igorrochap/rig/commit/bf070ee1ee6e9ae48cbfd7f461adfac4d239dd71))


## v1.3.0 (2026-08-17)

### Features

- **cli**: Add quiet mode for command output ([#34](https://github.com/igorrochap/rig/pull/34),
  [`574cd8b`](https://github.com/igorrochap/rig/commit/574cd8bc2551254c5f91ca78d7b368b111c8a2cd))


## v1.2.0 (2026-08-17)

### Features

- **cli**: Display identification banners for commands
  ([#33](https://github.com/igorrochap/rig/pull/33),
  [`838e240`](https://github.com/igorrochap/rig/commit/838e24008f5e6b4b02d7e96fbbc82c0483566e5e))


## v1.1.2 (2026-08-17)

### Bug Fixes

- **review**: Improve verdict parsing and error handling
  ([#32](https://github.com/igorrochap/rig/pull/32),
  [`acc0f22`](https://github.com/igorrochap/rig/commit/acc0f22bf182fcbb8b73bfa12ae26987a70edd5c))


## v1.1.1 (2026-08-17)

### Bug Fixes

- **orchestration**: Handle duplicated assistant output in stream
  ([#31](https://github.com/igorrochap/rig/pull/31),
  [`8897df9`](https://github.com/igorrochap/rig/commit/8897df903fa75a0eeaa64203d1c7b784b20776ae))


## v1.1.0 (2026-08-17)

### Bug Fixes

- **claude**: Enable verbose logging for requests ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

- **config**: Enforce claude- prefix for claude models
  ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

### Documentation

- **skills**: Update review documentation and process
  ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

- **workflow**: Remove redundant branch naming instructions
  ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

### Features

- **cli**: Improve orchestration streaming and model configuration
  ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

- **cli**: Label orchestration output by role ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

- **orchestration**: Stream reviewer output to console
  ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))

### Refactoring

- **claude**: Unify base CLI flags ([#25](https://github.com/igorrochap/rig/pull/25),
  [`8637edd`](https://github.com/igorrochap/rig/commit/8637edd4d7801c8c715fd600ec873022ec190b1e))


## v1.0.0 (2026-08-17)

- Initial Release
