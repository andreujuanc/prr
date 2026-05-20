# Changelog

## [1.8.0](https://github.com/andreujuanc/prr/compare/v1.7.0...v1.8.0) (2026-05-20)


### Features

* **ai:** step cap per ChatStream call (50 -&gt; 20) + per-call rounds log ([0853f24](https://github.com/andreujuanc/prr/commit/0853f24ecfc41fb5a60ba5e8c5bdc6a4607c0b26))
* **audit:** persist headless audit snapshot to .git/pr-tui/audits ([9b2aa7d](https://github.com/andreujuanc/prr/commit/9b2aa7dce458ee03e42882edf5dfcaca3ab6490f))
* **claudecode:** default sonnet to --effort low ([f862607](https://github.com/andreujuanc/prr/commit/f8626075d35aed0cc88a8e2643d92d6c369da8e9))
* **claudecode:** expose --effort via PRR_CLAUDE_EFFORT env var ([145806d](https://github.com/andreujuanc/prr/commit/145806dc6824a0d22041af0322e51be891e74ce9))
* **config:** centralize model pricing and correct Gemini rates ([a41d069](https://github.com/andreujuanc/prr/commit/a41d0699aa40f13cee08b95680c15ed1baaed774))
* **prompts:** make tool use discretionary in deep-review prompts ([afbac98](https://github.com/andreujuanc/prr/commit/afbac984793b743802a54ee2d025a1a30ad2da3f))
* **review:** Gemini context caching for the deep-review pipeline ([9b5d80a](https://github.com/andreujuanc/prr/commit/9b5d80aa5eefd1ebda44bbe708c40db40a8916a5))
* **review:** persist headless review snapshot to .git/pr-tui/reviews ([50b2904](https://github.com/andreujuanc/prr/commit/50b2904b068c978f1233da441221b0ecb3c7678a))
* **review:** TUI runs synthesis + validate + snapshot — matches headless ([72072c1](https://github.com/andreujuanc/prr/commit/72072c10fdefc240b63dbdddb9ef05a5d3eaf713))
* **security:** add U5 context tier to TestAOIContextLineComparison ([ce8e5c9](https://github.com/andreujuanc/prr/commit/ce8e5c9931379503a7225228b585908f373f4291))
* **security:** tighten AOI urgency criteria — default more aggressively to grouped ([b0515e1](https://github.com/andreujuanc/prr/commit/b0515e1e8418992acd62193094a9c676897b2a3a))
* **state:** add SaveReviewSnapshot / SaveAuditSnapshot ([824de6f](https://github.com/andreujuanc/prr/commit/824de6f9c60f06174f07d3e127530b59923af230))
* **ui:** verdict pill — high-contrast badge in the Review tab ([da9a34c](https://github.com/andreujuanc/prr/commit/da9a34c5962816f73c2ad54e7a7973396bfc2f62))


### Bug Fixes

* **ci:** unbreak the test + lint pipeline ([51096ad](https://github.com/andreujuanc/prr/commit/51096ad17cbeb3306332267cf9c5526e64e46bf6))
* **review:** disable context-cache call site (broken in 9b5d80a) ([6f40eac](https://github.com/andreujuanc/prr/commit/6f40eac3649d5354a02dc341e47dffa1f5c2adbd))
* **review:** take last complete JSON value when model emits multiple drafts ([7b3bfc7](https://github.com/andreujuanc/prr/commit/7b3bfc7adfa91b68b958e1ca7e1ce57b70837d3c))

## [1.7.0](https://github.com/andreujuanc/prr/compare/v1.6.1...v1.7.0) (2026-05-18)


### Features

* **audit:** list valid dimensions in the AOI prompt ([3ea087d](https://github.com/andreujuanc/prr/commit/3ea087df14a99aa17755edf7f6dee8468b4da786))
* **audit:** prefix line numbers in AOI scanner input ([d638ec1](https://github.com/andreujuanc/prr/commit/d638ec19c6e6ee8b7f47cca744f8230698175086))


### Bug Fixes

* address two findings from prr's self-review of PR [#26](https://github.com/andreujuanc/prr/issues/26) ([321da54](https://github.com/andreujuanc/prr/commit/321da54741447941df062981c44153add3190957))
* **audit:** skip cache when --focus/--exclude/--include narrows scope ([f5baa63](https://github.com/andreujuanc/prr/commit/f5baa631d85b0a245644c67884944779b3c82624))
* lifecycle + durability — signal handler, state Save, config write ([e6e711f](https://github.com/andreujuanc/prr/commit/e6e711f78e4e63116a9070a820b3f1d1ed98af41))
* medium-severity audit cleanups (visibility, validation, timeouts) ([46de5b4](https://github.com/andreujuanc/prr/commit/46de5b48bcc4f0c3efd735428a35ddd05a59badd))
* three silent data-loss bugs in boundaries, coverage, openai ([6a48b6d](https://github.com/andreujuanc/prr/commit/6a48b6d0d22546ce7309d67755efe58e994451cb))
* **ui:** don't cancel the opencode subprocess at goroutine exit ([6fff6bc](https://github.com/andreujuanc/prr/commit/6fff6bc5a803339fb0082972d5e4fe65b0aee3db))

## [1.6.1](https://github.com/andreujuanc/prr/compare/v1.6.0...v1.6.1) (2026-05-16)


### Bug Fixes

* gofmt — column alignment regressed after previous commit's edits ([0463cc5](https://github.com/andreujuanc/prr/commit/0463cc5b937b0416cb149737eaa2b526bfd53edc))

## [1.6.0](https://github.com/andreujuanc/prr/compare/v1.5.0...v1.6.0) (2026-05-16)


### Features

* **progress:** surface AOI/general split + fix counter collision ([eca777a](https://github.com/andreujuanc/prr/commit/eca777a7045ceffed5d8e1188e54033eebc13010))


### Bug Fixes

* address review findings — error handling, env-handling, idiomatic parsing ([0324a87](https://github.com/andreujuanc/prr/commit/0324a87a11c39c6948be288c1aa60f4ec9610af7))

## [1.5.0](https://github.com/andreujuanc/prr/compare/v1.4.0...v1.5.0) (2026-05-14)


### Features

* **review:** two-pass recheck pipeline with in-loop evidence verific… ([8035b74](https://github.com/andreujuanc/prr/commit/8035b747c31f8a998320d360efd95f29619d6e14))
* **review:** two-pass recheck pipeline with in-loop evidence verification ([dd76dcd](https://github.com/andreujuanc/prr/commit/dd76dcdf8f045f39c88bc92e90651a510ecb065d))

## [1.4.0](https://github.com/andreujuanc/prr/compare/v1.3.0...v1.4.0) (2026-05-14)


### Features

* add audit mode with AOI-driven pipeline, model picker, and progress UI ([28e4f34](https://github.com/andreujuanc/prr/commit/28e4f34fa5472df210530baf04af66648f99679b))
* add headless 'prr review &lt;number&gt;' command and refactor review pipeline ([374b811](https://github.com/andreujuanc/prr/commit/374b811a0e583d83fafe3919ab3440b890599499))
* add recheck phase, debug mode, AOI-driven PR review, and evidence tracking ([ef9f111](https://github.com/andreujuanc/prr/commit/ef9f111ceb8f004d6ba42dbe086109f813dc6c73))
* audit mode with AOI-driven pipeline, file classification, and shared review infrastructure ([7be7ffa](https://github.com/andreujuanc/prr/commit/7be7ffa60301a019cb9535aff1809abc7648e304))
* **audit:** add confirmation prompt when file count exceeds 200 ([e7ae339](https://github.com/andreujuanc/prr/commit/e7ae339eaee3584e77cff38f1023a7efeb781701))
* **audit:** add file classification phase and include test files in audit ([0b88a20](https://github.com/andreujuanc/prr/commit/0b88a201c4b6c07c9cb6eeedb59629561fa387c6))
* **audit:** add file-load classification helpers ([1e2dfac](https://github.com/andreujuanc/prr/commit/1e2dfacdd9ac7866be671f334d3fb9c077d37542))
* **audit:** add severity bar and category chart to terminal report ([a20f7c8](https://github.com/andreujuanc/prr/commit/a20f7c84c2b852a073d440a61146acb61367ca5c))
* **audit:** CollectFiles reports exclusion stats and transients ([3e022e3](https://github.com/andreujuanc/prr/commit/3e022e34e2a3f66f6bc8146fadb01c322a0c9b6b))
* **audit:** configurable concurrency, recheck/synthesis caching ([b3b5c43](https://github.com/andreujuanc/prr/commit/b3b5c4381dd27c81b1a996ff976fb6634d3275c0))
* **audit:** Phase 1 file-load guards + aggregate-fail ([014fe63](https://github.com/andreujuanc/prr/commit/014fe63e421597f2b87147e7b525e76072c89c50))
* **audit:** show repo name in audit progress UI header ([190ba92](https://github.com/andreujuanc/prr/commit/190ba9248e5ce2560d831ea3dbac04727b736a3d))
* **audit:** surface project context, synthesis, cross-cutting, and failed reviews in reports ([ccdf9e6](https://github.com/andreujuanc/prr/commit/ccdf9e6d4d8e9e125694a86546742b6f544e0611))
* **audit:** surface recall gap in synthesis + severity anchors ([00f97ed](https://github.com/andreujuanc/prr/commit/00f97edfa6c343a7e4a171a68cc2138bae7f9218))
* **audit:** synthesis retry + hierarchical partial tolerance ([29762bd](https://github.com/andreujuanc/prr/commit/29762bdaf03f2a612399d7e9b9c290e42399cfbd))
* **classify:** add FileTypeSQL distinct from repository ([801be57](https://github.com/andreujuanc/prr/commit/801be578125a60b12350c00bf5e7ff0618ba9348))
* **classify:** retry transient errors + surface silent drops ([f311e38](https://github.com/andreujuanc/prr/commit/f311e3873fb4db618d5dd7d463b94f0baca098e6))
* **classify:** window file content as top 50 + middle 50 ([e4d8203](https://github.com/andreujuanc/prr/commit/e4d82037381596811a0ce3c53987366367fecc5e))
* claude-code provider, PR brief, watchdog, shared model picker ([1fd4b4b](https://github.com/andreujuanc/prr/commit/1fd4b4b947dfba6c6263eaaa488f9389f79857be))
* **dbg:** compact mode by default; skip system prompts in --debug ([b932781](https://github.com/andreujuanc/prr/commit/b932781ac0d65913304ea3754a1a45d432b8c7f5))
* **dbg:** elide embedded file-content blocks in --debug prompts ([d44eedd](https://github.com/andreujuanc/prr/commit/d44eedd9437dc83515edc0448bccfa236b170534))
* harden review JSON parsing, deep-review caching, synthesis recovery ([80c998d](https://github.com/andreujuanc/prr/commit/80c998dc818b2a17adec8e5f826c8ff0ea937ff7))
* **progress:** persist phase-completion summaries on done ([f4bb52c](https://github.com/andreujuanc/prr/commit/f4bb52c130cd0a70f1c9ba63d1ea30ad12ed0128))
* **progress:** recheck counter and progress bar ([d981f47](https://github.com/andreujuanc/prr/commit/d981f47e27c77f2f4e1f1f74154e62a71c26e846))
* **project:** review-oriented sectioned context + AI-config extraction ([4737dbd](https://github.com/andreujuanc/prr/commit/4737dbdd49b74f1e092727314726d2b355f7ce13))
* **prompts:** add observability and web-security dimensions, expand deps and testing ([1d5ffe6](https://github.com/andreujuanc/prr/commit/1d5ffe64c798d1b537b5bd6572e6132f0539339c))
* **review:** aggregate-fail + per-AOI failure visibility ([d208c3f](https://github.com/andreujuanc/prr/commit/d208c3fd3b60ac3af3933cc5ee84016a0c5af1f6))
* **review:** severity anchors + project-conventions awareness ([745f1cb](https://github.com/andreujuanc/prr/commit/745f1cb977c6960c941cb16a4729ffe1787fe9bc))
* **review:** validate deep-review results (drops, fields, severity) ([3243974](https://github.com/andreujuanc/prr/commit/3243974045f3af8fb669a42b85f82dd24c2ad636))
* **security:** aggregate-fail + consolidated AOI failure surfacing ([83545f6](https://github.com/andreujuanc/prr/commit/83545f65911593d51feef6165f9320841aeb17d8))
* **security:** AOI output sanity (taxonomy, IDs, empty-audit) ([8b284a2](https://github.com/andreujuanc/prr/commit/8b284a20c9c649bf28544d9eda2a77e5c5aa9533))
* **security:** retry transient AOI errors + surface silent drops ([c1850c1](https://github.com/andreujuanc/prr/commit/c1850c1427c626efbb5d2c00c180b9ea1303f5f4))


### Bug Fixes

* address 11 security and reliability findings from audit ([ea20ad5](https://github.com/andreujuanc/prr/commit/ea20ad5711419481f6d03ea942cd125a88abd25d))
* **ai:** plug error handling and timeout gaps across providers ([2e15f13](https://github.com/andreujuanc/prr/commit/2e15f13424282ead30647ba4e227573e03127b29))
* **audit:** act on findings F-001 through F-003 from self-audit ([74b4552](https://github.com/andreujuanc/prr/commit/74b4552a366b80029506feee9cb0a8c0c333e5e5))
* **audit:** act on findings F-001 through F-004 from self-audit ([f57f79a](https://github.com/andreujuanc/prr/commit/f57f79a16461c04185fd45c84eb7b1de7c5aa8b5))
* **audit:** replace blocking file-count prompt with non-blocking TUI warning ([5a22ffe](https://github.com/andreujuanc/prr/commit/5a22ffebd5be853fdabc5d99e505e897016da145))
* **debug:** resolve {{TOOLS}} before invoking LLM debug hooks ([a1e04f3](https://github.com/andreujuanc/prr/commit/a1e04f346e83a24a6a7135aced64f9ad450ae181))
* include finding ID (F-001) in audit report headings ([3a3e2f5](https://github.com/andreujuanc/prr/commit/3a3e2f51fc9c8d5c48ac9529d153cc136c952d58))
* **project:** fail loud on LLM error; never fall back to raw doc dump ([d19f4f9](https://github.com/andreujuanc/prr/commit/d19f4f9b275ae698665915a8eca9b9c945ff47a7))
* **review:** parse failures no longer poison the deep-review cache ([4ed2b7a](https://github.com/andreujuanc/prr/commit/4ed2b7a6cc9629f5fcfd2295bff717876b67ba86))
* **review:** sanitize fenced LLM output before persisting to RawMessage ([e10f0b2](https://github.com/andreujuanc/prr/commit/e10f0b26e3a693bd29d7e6754797237cf04072d5))
* **security:** tighten secret handling in update script, config, and API errors ([877842b](https://github.com/andreujuanc/prr/commit/877842b392874af2d6641db681cea956b49602d1))
* **state:** clear DeepFindings in ClearAllCaches ([2eaedbe](https://github.com/andreujuanc/prr/commit/2eaedbe19dabe295857c25e3864e9ef073f6718f))


### Performance Improvements

* **audit:** parallelize phase 0+1, recheck batches, and hierarchical synthesis ([cd954e3](https://github.com/andreujuanc/prr/commit/cd954e3053ee86c90b463c00f5018c11ebd4832f))

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
