# 修改记录 / Changelog

## v0.7.7

### 功能（AC 侧）

- **「修改管理 IP」弹窗**：设备操作弹窗新增入口（置于动作列表首位），打开专用对话框按模板 static 接口逐个填写管理地址（如 `10.10.10.252`，可带 `/24` 前缀），留空＝模板地址自动错开。支持「仅保存」（写入本机覆盖，重新下发后生效）与「保存并下发」（保存后立即走预检确认 → 重推，地址迁移与 IP 冲突照常弹窗，冲突时允许强制下发）。未采纳设备显示提示而不渲染编辑区。
  - 实现上把端口覆盖面板的地址行渲染与保存合并逻辑重构为共用的 `renderIPRows(host, dev)` / `mergeIPInputs(ov, host)`，弹窗与「端口 / 设备覆盖」面板行为完全一致；PUT 复用既有 `PUT /devices/{mac}/network` 与地址格式校验，后端无改动。

### English

- **"Edit management IP" dialog**: the device-actions dialog gained a dedicated entry (first in the list) that opens a per-device editor: one field per static template interface (e.g. `10.10.10.252`, optionally with a `/24` prefix); empty means auto-stagger from the template address. Supports "save only" (writes the unit override, applied on the next push) and "save and push" (save, then the usual preflight confirm → repush, with address-move confirmation and IP-conflict warning that can still be forced). Unadopted devices show a hint instead of the editor.
  - The row rendering and save-merge logic are refactored into shared `renderIPRows(host, dev)` / `mergeIPInputs(ov, host)` used by both this dialog and the port-override panel; the backend is unchanged (existing `PUT /devices/{mac}/network` with its address validation).

## v0.7.6

### 功能（AC 侧）

- **每设备自定义管理地址 + 冲突告警可强制下发**：设备覆盖面板新增「管理地址」编辑区，按模板接口逐个填写（留空即保持默认的自动错开）。手填地址若与其他设备已占用地址重复，下发前的预检弹窗会以「管理地址冲突」列出每个冲突的 IP、接口与被占用设备，操作员确认后可**强制下发**；取消则不下发。默认路径保持不变：留空时控制器在同网段自动分配空闲地址。
  - 接口：`POST /devices/{mac}/adopt` 与 `/repush` 接受 `{"force":true}`；不带 force 的冲突返回 409 并附 `conflicts` 数组（`ip`/`interface`/`device`），由 UI 直接渲染，不再依赖解析错误文本。
  - 预检 `GET /devices/{mac}/preflight` 冲突时不再报错，改为在 `conflicts` 字段返回清单（`will_move` 语义不变）。
  - `PUT /devices/{mac}/network` 校验地址格式（`10.0.0.1` 或 `10.0.0.1/24`），错写地址在入口即被拒绝，不会写进 AP 的 network 配置。
  - 强制下发会记一条 `ipconflict` 事件，便于事后审计。

### English

- **Per-device custom management address, with a conflict warning that can still be forced**: the device-override panel gained a "management address" editor (one field per template interface; leave it empty to keep the default auto-stagger). If a typed address duplicates one another unit already uses, the pre-push preflight dialog lists every conflict — IP, interface, and the device holding it — and lets the operator push anyway. Default behaviour is unchanged: empty fields mean the controller allocates a free address in the subnet.
  - `POST /devices/{mac}/adopt` and `/repush` accept `{"force":true}`; an unforced conflict returns 409 with a `conflicts` array (`ip`/`interface`/`device`) the UI renders directly.
  - `GET /devices/{mac}/preflight` reports conflicts in a `conflicts` field instead of failing.
  - `PUT /devices/{mac}/network` validates address syntax, so a typo cannot reach the AP's network file.
  - A forced duplicate logs an `ipconflict` event for later audit.

## v0.7.5

### 功能（AC 侧）

- **模板下发自动错开管理 IP**：同一份网络模板下发给多台 AP 时不再重复同一静态地址——首台保留模板基址，后续设备在同网段内自动分配下一个空闲主机地址（基址、基址+1……直至最后一个主机地址，超过后回绕到最小主机地址），并写入该设备的 `network_params.ipaddr` 覆盖，重推、预检结果稳定不变。占用集合包括所有设备的 per-unit 覆盖与已采纳设备实际渲染的模板地址（pending 设备的覆盖同样保留）。操作员显式填写的地址若与其他设备冲突，采纳/重推会被拒绝并指名占用该地址的设备，而不是静默改写。预检（preflight）展示的是错开后的实际地址。设备网络参数页可见自动分配的地址，可随时改。

### 修复（AP 侧）

- **状态卡显示 agent 版本与配套 AC 管理器要求**：原「agent」行只显示裸版本号，现改为「agent 版本」行，显示 `ac-agent <版本>`、二进制所讲的协议版本（status.json 新增 `protocol_version` 字段）与启动时间；新增「配套 AC 管理器」行，提示需要 v0.7.0 及以上的控制器（协议版本 9），且管理器与 agent 应一并升级。若 agent 上报的协议版本与本界面预期不一致，额外显示警告条。
- **「重置纳管」改为红色按钮**：卡片触发按钮与二次确认弹窗中的提交按钮均使用 `cbi-button-negative` + 内联红色（#c62828），在所有主题下稳定呈红色，强调破坏性操作。
- **按钮溢出修复（argon 兼容）**：v0.7.3 的内联负边距修正在 argon 主题下仍会溢出，现彻底弃用 `cbi-page-actions`，改用自带 flex 布局（右对齐、8px 间距、可换行）的容器，不依赖任何主题样式。
- 软件包版本：`ac-agent 0.3.0-3`（状态文件新增协议版本字段）、`luci-app-ac-agent 1.0.0-8`。旧版 agent 不上报协议版本时，界面回退显示"请将 ac-agent 与 AC 管理器一并升级"。

### English

- **Auto-stagger duplicate template IPs**: when the same network-managing template is pushed to multiple APs, the first unit keeps the template's base address and every following unit is auto-assigned the next free host in the same subnet (wrapping past the broadcast address when needed). The allocation is persisted into the unit's `network_params.ipaddr` override, so re-pushes and preflights stay stable. Operator-written overrides are never rewritten — a colliding explicit address is refused with the name of the device already holding it. Preflight reports the staggered address.
- The status card now shows the agent version with the wire protocol it speaks (`protocol_version` added to status.json) and a "Matching controller" row requiring controller v0.7.0+ (protocol version 9); a protocol mismatch between the running agent and the interface raises a warning banner.
- "Reset management" is now a red button — both the card trigger and the confirmation submit (`cbi-button-negative` plus inline red so it stays red in every theme).
- The action-row overflow under argon is fixed for good: the card no longer reuses `cbi-page-actions` (whose theme-specific negative margins caused the issue); it renders its own right-aligned, wrapping flex row instead.
- Packages: `ac-agent 0.3.0-3`, `luci-app-ac-agent 1.0.0-8`. Agents older than protocol reporting fall back to combined-upgrade advice.

## v0.7.4

### 修复

- **二次确认弹窗点击无反应**：v0.7.2 引入的 `uiConfirm` 助手有两处缺陷——确认/取消回调引用了未定义的收尾函数（点击即抛错，Promise 永不兑现）；确认按钮 `className` 整体赋值抹掉了稳定的 `ui-confirm-ok` 选择器类（第二次打开弹窗时找不到按钮，弹窗打不开）。两处均已修复，并经 Playwright 无头浏览器实测五条路径：确认、取消、primary 变体、复用元素二次打开、Esc 关闭。

### English

- Fixed two defects in the v0.7.2 `uiConfirm` helper (undefined resolve handler; destructive className assignment dropping the stable ok-button class). All five interaction paths verified headlessly: ok, cancel, primary variant, element reuse on reopen, Escape.

### 下载 / Downloads

- AC 容器镜像：`naoki66/openwrt_ac-controller:v0.7.4` 或 `ghcr.io/naoki66/openwrt_ac-controller:v0.7.4`。

## v0.7.3

### 修复（AP 侧）

- **删除设备后 AP 的 401 死循环自愈**：AP 已绑定时若控制器上的记录被删除（409 恢复操作、数据库回滚），agent 会永远收到 `hello: controller returned 401`。现在连续 3 次心跳 401 后，agent 自动放弃管理绑定（保留纳管身份与控制器钉定），以同一身份重新登记为待采纳，采纳后恢复管理。
- **LuCI 状态卡按钮溢出**：状态卡底部「重启 agent / 重置纳管」按钮受主题 `cbi-page-actions` 负边距影响溢出面板边缘，改为内联样式约束在卡片内。
- 软件包版本：`ac-agent 0.3.0-2`、`luci-app-ac-agent 1.0.0-7`。当前受影响的 AP 可直接点「重置纳管」立即恢复，无需等待升级。

### English

- Bound agents whose controller record was deleted now drop the management binding after three consecutive hello 401s and re-enroll with the same identity; adoption restores management. The LuCI status card no longer lets its action buttons overflow the panel. Packages: `ac-agent 0.3.0-2`, `luci-app-ac-agent 1.0.0-7`.

### 下载 / Downloads

- AP 安装本次 Release 的 `ac-agent` 与 `luci-app-ac-agent`；AC 侧无代码变更，容器镜像可按需更新 `naoki66/openwrt_ac-controller:v0.7.3`。

## v0.7.2

### 修复

- **设备操作弹窗风格统一**：采纳/重新下发的地址变更确认、解绑、删除设备、重启无线、重启 AP、恢复出厂全部改用站点「危险操作二次确认」弹窗（`ui-confirm` 样式），移除原生 `confirm` 与旧版 capture 拦截（此前危险按钮会先后弹样式化确认与原生确认两道弹窗，且拦截文案与动作不符）。

### English

- Device-action confirmations (address move, release, delete, wifi reload, reboot, factory reset) now use the site's styled confirm dialog instead of the browser-native `confirm()`; the legacy capture-interception layer is removed.

### 下载 / Downloads

- AC 容器镜像：`naoki66/openwrt_ac-controller:v0.7.2` 或 `ghcr.io/naoki66/openwrt_ac-controller:v0.7.2`。

## v0.7.1

### 修复

- **整机重刷后的 409 死锁**：AP 重刷/换机后设备密钥变更、重置代次归零，控制器上保留的旧纳管绑定会让登记永远报 `this MAC is already registered to another AP identity`，「解绑」无法清除。新增管理动作 `DELETE /api/v1/devices/{mac}`（设备操作弹窗「删除设备」），整行删除纳管绑定、客户端与遥测历史；删除后 AP 以新身份重新登记待采纳。无绑定记录的 MAC 现在接受全新登记（此前空绑定也会被 409 拒绝）。Agent 端 409 报错改为可操作指引。
- 定时任务（状态 LED / 周期重启）按控制器本地时区计算；文档补充容器 `TZ` 写法与 8 小时偏差的排查（本次生产事故源于部署 compose 手写了非法 `TZ` 值，模板本身正确）。

### 升级说明

- 软件包版本不变（AP 侧无代码变更，仅 Agent 报错文案）；AC 升级容器镜像即可。

### English

- New admin action `DELETE /api/v1/devices/{mac}` clears the enrollment binding kept for a re-initialised AP, which previously dead-locked enrollment with a 409 forever. Enrollment now accepts a fresh claim for a MAC without a stored binding. The agent's 409 message explains the recovery. Scheduling docs now cover container TZ and the +8h symptom.

### 下载 / Downloads

- AC 容器镜像：`naoki66/openwrt_ac-controller:v0.7.1` 或 `ghcr.io/naoki66/openwrt_ac-controller:v0.7.1`。

## v0.7.0

### 自动发现与采纳

- 未纳管或刚重置的 AP 通过 UDP 29820 广播型号、MAC、管理地址、设备公钥和 Agent 版本。AC 仅回复登记地址；AP 通过一次性挑战证明自己持有对应私钥后，AC 才创建或更新设备记录。广播里没有私钥或纳管密钥。
- 管理员在 AC 点「采纳」即可接入。每个 AC 部署首次启动时自动生成一把纳管密钥并保存在 SQLite，向该 AC 已采纳的所有 AP 加密下发同一把密钥。保留 AC 数据目录即可沿用密钥。绑定后 AP 改发认证心跳；需要重新定位 AC 时，只查找已经绑定的 AC。

### 重置后重新发现

- AP 的 LuCI 新增「重置纳管」。重置只清除 AC 绑定，保留当前 Wi‑Fi、网络和 AP 设备身份，并递增持久化的重置代次，随后恢复待采纳广播。不需要事先通知原 AC。
- 原 AC 验证同一设备身份和更高代次后，将同一条设备记录转回「待采纳」，清理旧的应用与升级状态，让待执行命令失效，并记一条「重置纳管」事件。配置版本历史保留，管理员须重新点击「采纳」。
- 旧代次的登记、同代次的重复登记，以及重置前已经开始但尚未完成的采纳，都不能覆盖新的绑定状态。

### 升级说明

- 本次协议版本为 9；请配套升级 AC、`ac-agent` 和 LuCI，保留 AC 数据目录、AP 的 `/etc/ac/state.json` 和 `/etc/ac/enrollment.key`。旧的自动纳管记录按代次 0 读取；升级本身不会触发重置。原有手填共享密钥模式仍可继续运行。
- 软件包版本：`ac-controller 0.2.0-1`、`ac-agent 0.3.0-1`、`luci-app-ac-agent 1.0.0-6`。
- 原 AC 必须能收到 AP 的新登记才会更新记录；AP 若已移到原 AC 不可达的网络，原记录仍显示离线。

### English

- Unbound and reset APs broadcast public device metadata. The AC changes enrollment state only after a fresh proof of identity. Adoption delivers the same persistent deployment key to every approved AP on that AC.
- LuCI **Reset management** preserves Wi-Fi, networking and AP identity. A verified newer reset returns the existing record to **Pending** on any AC that receives it, expires queued commands and requires fresh approval. Stale registrations and approvals cannot replace the new binding.
- Update the AC, agent and LuCI together. Keep the AC data directory and the AP's enrollment state and identity key. Manual shared-secret deployments remain supported.

### 下载 / Downloads

- AC 容器镜像：`naoki66/openwrt_ac-controller:v0.7.0` 或 `ghcr.io/naoki66/openwrt_ac-controller:v0.7.0`。
- AP 安装本次 Release 的 `ac-agent` 和 `luci-app-ac-agent` 软件包；中文界面另装 `luci-i18n-ac-agent-zh-cn`。用 `SHA256SUMS` 校验，构建信息见 `build-info.txt`。

Earlier releases / 历史版本：[GitHub Releases](https://github.com/naoki66/OpenWrt_AC-Controller/releases).
