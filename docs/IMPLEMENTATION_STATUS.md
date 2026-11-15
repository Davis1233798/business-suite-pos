# 實作狀態：2026-10-30

## POS 收銀 UI 增量：2026-11-15

新增 `/pos/front` 收銀垂直切片：限定範圍終端資料、開班、條碼掃描、數量與固定單行折讓、現金結帳及伺服器回傳收據。開班與結帳遇不確定結果時鎖住輸入並用原冪等鍵重試；唯讀角色不能寫入。收銀頁不載入通用 `pos/data?view=front` 資料集。此交付不代表 P01–P08 整體驗收或正式部署。

本次驗證：`npm run go:test`、`npm test`、`npx tsc --noEmit`、`npx playwright test --config playwright.cashier.config.ts`（5/5）、`npm run web:build`、`npx eslint web/src/PosCashier.tsx` 與 `git diff --check` 通過。`web/src/main.tsx` 全檔 ESLint 仍有既存錯誤，新增路由段沒有 lint 診斷。尚未設計折讓原因、主管核准、促銷規則或折讓上限；完整端到端 P01–P08、離線交易、多工作站競爭及生產部署仍未完成／驗證。細節見 `docs/engineering/POS_CASHIER_UI.md` 與 `docs/engineering/POS_CART_LINE_DISCOUNTS.md`。

## P04 掛單 UI 增量：2026-11-15（本機未合併）

目前 `codex/pos-p04-held-carts` 工作樹增加掛單清單與暫存、競取、取回、釋出、修正及作廢。Go 僅列同店 held carts 和登入者自己的 retrieved carts；自己的已取回掛單在最多 100 筆清單中優先，方便重整後接續。更正由原 claim owner 提交，Go 依當前商品價格驗證並原子更新；作廢僅能由 claim owner 執行，記錄稽核及冪等結果，不建立銷售或扣庫。

本分支驗證：`go test ./... -count=1` 通過；`npx playwright test --config=playwright.cashier.config.ts` 18/18；`npx tsc --noEmit`、`npm run web:build`、`npx eslint web/src/PosCashier.tsx` 與 `git diff --check` 通過。`npx playwright test --config=playwright.lifecycle.config.ts` 3/3 通過，使用獨立 Go API、Vite 與同源本機代理。另以全新暫存 SQLite 執行真實 Go API／瀏覽器掛單流程：暫存、取回、折讓更正、重新載入後恢復及結帳皆成功；瀏覽器收據顯示伺服器金額 subtotal 200、折讓 15、應收 185，SQLite 確認掛單 completed 且現金銷售金額 185。Playwright 競取仍是兩個瀏覽器 context 共用 mock API；真實 SQLite 競取由 Go API 測試覆蓋。此工作尚未合併／部署；跨店轉移、關班後恢復、離線交易與完整 P01–P08 驗收未涵蓋。詳見 `docs/engineering/POS_HELD_CART_UI.md`。

本次由目前 Codex 主代理與三個同模型子代理完成整合，未使用不可用的指定替代模型。

已實作個人作品首頁、MES、ERP、POS、CRM、餐飲與電商前後台；使用真正 D1 持久化、訪客展示空間、模擬角色、原子庫存／付款／報工交易、種子重置、完整業務備份還原及 CSV。

驗證：npx tsc --noEmit 通過；npm test 28/28 通過；Sites build-site 正式建置成功；scripts/smoke-http.mjs 對本機正式 Worker 8787 完整通過。POS 瀏覽器操作確認兩件100折10、收200找10。

角色切換僅供隔離展示，並非正式企業身分權限。付款／物流為明確模擬。未把舊測試名稱 E01-E08 等視為完整驗收；各項限制見 docs/ACCEPTANCE.md。OEE 現頁面為標示清楚的固定計算示例。MES 進階排程、完整重工、CRM 合併與細分業務權限、電商分段履約及正式外部串接仍待擴充。

部署：原 Site ID 沿用，私人權限保留；發布終態另以原生 Sites 回傳為準。

發布阻礙：2026-10-30 最後原生 Sites get_site 與 create_source_repository_write_credential 均回 404 project_not_found；因此未發布、未捏造 URL，也未建立重複 Site。既有 project_id 保留。正式 Worker 本機預覽 http://127.0.0.1:8787/ 可用。瀏覽器最終視覺檢查被 Work settings 停用；先前 POS 瀏覽器操作已通過。
