# Changelog

## [1.3.0](https://github.com/andreujuanc/prr/compare/v1.2.0...v1.3.0) (2026-05-06)


### Features

* add GitHub Actions status panel with live polling ([dedaf9c](https://github.com/andreujuanc/prr/commit/dedaf9c8ac013b91558eb428ed9e0d233f324249))
* Fix with OpenCode - background task system with Tasks tab ([1d5ece7](https://github.com/andreujuanc/prr/commit/1d5ece74898aa4d9e0d887af2c04355ca89a9022))
* GitHub Actions, inline findings, publish, and OpenCode Fix integration ([d23e7fa](https://github.com/andreujuanc/prr/commit/d23e7fa1c0181b3681a3a5fbeedb9378fb9fff6f))
* inline review findings in diff view with toggle ([5172bad](https://github.com/andreujuanc/prr/commit/5172bad1849fc9510337b8d7763193e97c3bf86c))
* **project:** auto-discover project context for batch review injection ([a20638e](https://github.com/andreujuanc/prr/commit/a20638e2091c6ebb6923e2bea13b0c2f7268aeab))
* **prompts:** enhance all review dimensions with expert personas and business logic checks ([cff791b](https://github.com/andreujuanc/prr/commit/cff791be8694f286cc946698a5fe10fb4b0d352a))
* **prompts:** enhance security prompts inspired by Vercel DeepSec ([22acf9d](https://github.com/andreujuanc/prr/commit/22acf9d1e048ae4021d0e4ce5007c54141377d24))
* proxy OpenCode permissions and questions to TUI modals ([d9d4dd7](https://github.com/andreujuanc/prr/commit/d9d4dd7e4a0e3afee9bee1bcf5a0d1169f43ceaf))
* publish findings as GitHub comments and pipe to external processes ([16490aa](https://github.com/andreujuanc/prr/commit/16490aae3268c0789b964bb36e87f6c56870a0d5))
* render opencode task output as markdown with colors ([7d335a5](https://github.com/andreujuanc/prr/commit/7d335a522808d9996a42173613956985a6b3eb97))
* **security:** add AOI model profiles with benchmark-tuned settings ([93fdf63](https://github.com/andreujuanc/prr/commit/93fdf630b29c19e4e4f3112f4ab9dcdbf443cb9d))
* **security:** cache AOI results per file by diff hash ([bce6367](https://github.com/andreujuanc/prr/commit/bce636718041ff857b45583bda392eb5ac2cc207))
* **ui:** thread review comment replies and update keybinding help text ([1e41df9](https://github.com/andreujuanc/prr/commit/1e41df9f1e3fed5b9b770c17c62da782602296cd))


### Bug Fixes

* eliminate data races and stderr loss in background task system ([dc0fab0](https://github.com/andreujuanc/prr/commit/dc0fab0b55549683cc09eb71123dc1ce4ee23896))
* eliminate race condition in opencode Manager.Start ([d3d6d03](https://github.com/andreujuanc/prr/commit/d3d6d03e026dae163f365b30dc1095c53395e82c))
* kill opencode server when no tasks running and on prr exit ([3cb4e43](https://github.com/andreujuanc/prr/commit/3cb4e436b1a280a318b890748367b474449cc08a))
* prevent diff pane content from overflowing into right panel ([2e60bbc](https://github.com/andreujuanc/prr/commit/2e60bbc9d4a9a6e1c531a6b7afced3ed3b7932ad))
* stop task timer on completion and remove incorrect 'opencode run' text ([9ef6287](https://github.com/andreujuanc/prr/commit/9ef628797808c4cd6e5bb152e391d770c78bcb88))
* **ui:** clear AOI cache on re-review and fix duplicate case in key handler ([14db495](https://github.com/andreujuanc/prr/commit/14db495b29d9983a96b0383d5088a9957b244f44))
* **ui:** do not clear previous review before streamMultiPassReview ([0848a9d](https://github.com/andreujuanc/prr/commit/0848a9d530fbb28f9c35f7eae7ea3e770689a51c))
* **ui:** sequential AOI progress display and layout edge cases ([574ebac](https://github.com/andreujuanc/prr/commit/574ebac10965ef7e2cd7890b8a5dee3146a2c2c8))


### Performance Improvements

* **security:** parallelize AOI batch scanning (up to 5 concurrent) ([a54a042](https://github.com/andreujuanc/prr/commit/a54a042b82144d7c15a6dc3c8d65305f745a5440))

## [1.2.0](https://github.com/andreujuanc/prr/compare/v1.1.0...v1.2.0) (2026-05-01)


### Features

* theme system, git blame, chroma renderer, and UX improvements ([0bacaa3](https://github.com/andreujuanc/prr/commit/0bacaa3b0ffe305c9a54d04290ff94dffcb745e3))
* theme system, git blame, chroma renderer, and UX improvements ([423ab4f](https://github.com/andreujuanc/prr/commit/423ab4f77c47330522e89adbe3e99e2b276638a9))


### Bug Fixes

* error modal for review submission, dynamic chroma separator width ([0683c85](https://github.com/andreujuanc/prr/commit/0683c85e42e2e86382d14b728a23bb98cdeef17b))

## [1.1.0](https://github.com/andreujuanc/prr/compare/v1.0.1...v1.1.0) (2026-05-01)


### Features

* update all dependencies ([9628845](https://github.com/andreujuanc/prr/commit/96288451a23e781cd6fe04024717b954df76e7ae))
* update dependencies, code review cleanups, and TUI e2e tests ([a3f169e](https://github.com/andreujuanc/prr/commit/a3f169e0984393879a4949701eab7357bc66d53c))

## [1.0.1](https://github.com/andreujuanc/prr/compare/v1.0.0...v1.0.1) (2026-05-01)


### Bug Fixes

* gofmt all files and skip git_log rev_range test on shallow clones ([5945668](https://github.com/andreujuanc/prr/commit/59456685025cf1189e640b8a6c12fb062a209324))
* use gemini-2.5-flash as default model, sync tests, fix goreleaser deprecation ([03c090b](https://github.com/andreujuanc/prr/commit/03c090b87b000ebd5d182b86bc77576e9edd691a))
* use gemini-2.5-flash as default model, sync tests, fix goreleaser deprecation ([66e18ee](https://github.com/andreujuanc/prr/commit/66e18ee1014c673eaa2d5fdef0033bd01ec79be1))

## 1.0.0 (2026-05-01)


### Bug Fixes

* improve CI, GoReleaser config, and add release-please for automated versioning ([b5432be](https://github.com/andreujuanc/prr/commit/b5432beab8db434fc801d1ac5b02e8c2ab655385))
