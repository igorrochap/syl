# CHANGELOG

<!-- version list -->

## v1.14.0 (2026-08-20)

### Features

- **cli**: Add self-update command ([#60](https://github.com/igorrochap/syl/pull/60),
  [`21a2ced`](https://github.com/igorrochap/syl/commit/21a2ced55718835a0b89d1d49ad23a5ced63697a))


## v1.13.0 (2026-08-20)

### Features

- **orchestration**: Support custom branch name suggestions
  ([#59](https://github.com/igorrochap/syl/pull/59),
  [`27eb0a7`](https://github.com/igorrochap/syl/commit/27eb0a789a1dc2b2f3c08316628d95109679eb0e))


## v1.12.0 (2026-08-20)

### Features

- **cli**: Add version command and build metadata ([#58](https://github.com/igorrochap/syl/pull/58),
  [`1d7b4c4`](https://github.com/igorrochap/syl/commit/1d7b4c411f984f712f5807d28c7b91c59fe61ac6))


## v1.11.0 (2026-08-19)

### Chores

- **config**: Update harness model and effort settings
  ([#55](https://github.com/igorrochap/syl/pull/55),
  [`2acd8fe`](https://github.com/igorrochap/syl/commit/2acd8fed993ca75c3128b8cbdfc53e7a35a22d47))

- **deps**: Changes implementer model and effort ([#55](https://github.com/igorrochap/syl/pull/55),
  [`2acd8fe`](https://github.com/igorrochap/syl/commit/2acd8fed993ca75c3128b8cbdfc53e7a35a22d47))

### Documentation

- **review**: Clarify coordinator contract and workflow
  ([`bab9c26`](https://github.com/igorrochap/syl/commit/bab9c2606bcb27ab6da36e5830da2686d77cb24a))

### Features

- **cli**: Implement skill synchronization ([#55](https://github.com/igorrochap/syl/pull/55),
  [`2acd8fe`](https://github.com/igorrochap/syl/commit/2acd8fed993ca75c3128b8cbdfc53e7a35a22d47))


## v1.10.0 (2026-08-19)

### Chores

- **config**: Update harness model and effort settings
  ([#54](https://github.com/igorrochap/syl/pull/54),
  [`6437a4f`](https://github.com/igorrochap/syl/commit/6437a4f78d9e86babe3f186a71a2d6bf7eea6d28))

### Documentation

- **review**: Clarify coordinator contract in skill guide
  ([#53](https://github.com/igorrochap/syl/pull/53),
  [`ec3ae0f`](https://github.com/igorrochap/syl/commit/ec3ae0fb00342bcba6deccf4602873e352c1c3b7))

### Features

- **cli**: Implement interactive planning workflow
  ([#54](https://github.com/igorrochap/syl/pull/54),
  [`6437a4f`](https://github.com/igorrochap/syl/commit/6437a4f78d9e86babe3f186a71a2d6bf7eea6d28))


## v1.9.1 (2026-08-19)

### Bug Fixes

- **orchestration**: Include untracked files in review diff
  ([#52](https://github.com/igorrochap/syl/pull/52),
  [`ee821be`](https://github.com/igorrochap/syl/commit/ee821bee6283cd7e5cfcee54c1cb86ca4ff1f1d5))

### Refactoring

- **orchestration**: Abstract run artifacts into recorder
  ([#47](https://github.com/igorrochap/syl/pull/47),
  [`3436efa`](https://github.com/igorrochap/syl/commit/3436efac24a49d1b33f2df2804192f41b67e907f))

- **orchestration**: Centralize prompt templates ([#48](https://github.com/igorrochap/syl/pull/48),
  [`b7c65f0`](https://github.com/igorrochap/syl/commit/b7c65f0266535369e65f0724ed0fb0e99e1e21a0))

- **orchestration**: Extract presentation logic ([#46](https://github.com/igorrochap/syl/pull/46),
  [`3d04f4f`](https://github.com/igorrochap/syl/commit/3d04f4f54febf027cad50afb56c2518c773f7f73))

- **orchestration**: Move branch naming logic to dedicated file
  ([#49](https://github.com/igorrochap/syl/pull/49),
  [`7cd9cc8`](https://github.com/igorrochap/syl/commit/7cd9cc80d3b9ef22e42ced91c8b3734e1b233853))


## v1.9.0 (2026-08-19)

### Features

- **cli**: Support bare numbers for issue references
  ([`a795092`](https://github.com/igorrochap/syl/commit/a79509261cb5ae43bbfc561a21b6220d1e530e7c))


## v1.8.0 (2026-08-18)

### Features

- **orchestration**: Support resuming reviewer sessions
  ([#45](https://github.com/igorrochap/syl/pull/45),
  [`9b0432b`](https://github.com/igorrochap/syl/commit/9b0432ba5504c09715cae0ec7f3702cdbff03509))


## v1.7.1 (2026-08-18)

### Bug Fixes

- **orchestration**: Deduplicate session IDs during recording
  ([#44](https://github.com/igorrochap/syl/pull/44),
  [`1a29b9e`](https://github.com/igorrochap/syl/commit/1a29b9e28d8183920acb699e4be2d7d08a808262))


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
