# 修改记录 / Changelog

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
