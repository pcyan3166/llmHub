import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { Provider, useDispatch, useSelector } from "react-redux";
import {
	LayoutDashboard,
	FolderKanban,
	Workflow,
	Layers3,
	ListOrdered,
	ScrollText,
	ChartNoAxesCombined,
	KeyRound,
	Settings,
	Plus,
	Pencil,
	Trash2,
	RefreshCw,
	LogOut,
	Copy,
	Download,
	ArrowUpRight,
	Check,
	Search,
	Menu,
	Server,
	ShieldCheck,
	Globe,
} from "lucide-react";
import {
	api,
	store,
	authenticate,
	errorMessage,
	useConfigQuery,
	usePricingQuery,
	useOverviewQuery,
	useKeysQuery,
	useSaveMutation,
	useCreateKeyMutation,
	useRevokeKeyMutation,
} from "./api";
import { Editor, Modal } from "./views/editor";
import { CatalogView } from "./views/catalog";
import type { Entity, Config, Attempt } from "./types";
import "./style.css";

const nav = [
	{ id: "overview", label: "总览", icon: LayoutDashboard },
	{ id: "projects", label: "项目", icon: FolderKanban },
	{ id: "scenes", label: "业务场景", icon: Workflow },
	{ id: "profiles", label: "模型 Profiles", icon: Layers3 },
	{ id: "catalog", label: "官方模型与价格", icon: Globe },
	{ id: "pools", label: "共享队列", icon: ListOrdered },
	{ id: "requests", label: "请求记录", icon: ScrollText },
	{ id: "usage", label: "用量与成本", icon: ChartNoAxesCombined },
	{ id: "keys", label: "项目密钥", icon: KeyRound },
	{ id: "settings", label: "设置", icon: Settings },
];
const usd = (v: number) => `$${v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 6 })}`;
const number = (v: number) => v.toLocaleString("en-US");
const localMonth = () => {
	const d = new Date();
	return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`;
};
function App() {
	const dispatch = useDispatch();
	const token = useSelector((s: ReturnType<typeof store.getState>) => s.session.token);
	const [draftToken, setDraftToken] = useState("");
	const [page, setPage] = useState(new URLSearchParams(location.search).get("view") ?? "overview");
	const [month, setMonth] = useState(localMonth());
	const [project, setProject] = useState("");
	const [search, setSearch] = useState("");
	const [mobile, setMobile] = useState(false);
	const [editor, setEditor] = useState<{ entity: Entity; row?: Record<string, unknown> }>();
	const [removal, setRemoval] = useState<{ entity: Entity; id: string; project_id?: string }>();
	const [keyProject, setKeyProject] = useState("");
	const [secret, setSecret] = useState("");
	const [copied, setCopied] = useState(false);
	const [notice, setNotice] = useState("");
	const [selected, setSelected] = useState<Attempt>();
	const [revoke, setRevoke] = useState<string>();
	const configQuery = useConfigQuery(undefined, { skip: !token, pollingInterval: 30000, skipPollingIfUnfocused: true });
	const pricingQuery = usePricingQuery(undefined, {
		skip: !token || page !== "profiles",
		pollingInterval: 5000,
		skipPollingIfUnfocused: true,
		refetchOnMountOrArgChange: true,
	});
	const overviewQuery = useOverviewQuery({ month, project }, { skip: !token, pollingInterval: 5000, skipPollingIfUnfocused: true });
	const keysQuery = useKeysQuery(undefined, { skip: !token });
	const [save, saveState] = useSaveMutation();
	const [createKey, createState] = useCreateKeyMutation();
	const [revokeKey] = useRevokeKeyMutation();
	const config = configQuery.data?.config;
	const overview = overviewQuery.data;
	const logout = () => {
		dispatch(authenticate(""));
		dispatch(api.util.resetApiState());
		setSecret("");
		setDraftToken("");
	};
	const navigate = (view: string) => {
		setPage(view);
		setSearch("");
		setMobile(false);
		history.replaceState(null, "", `?view=${encodeURIComponent(view)}`);
	};
	const commit = async (next: Config) => {
		if (!configQuery.data) return;
		try {
			await save({ config: next, version: configQuery.data.version }).unwrap();
			setNotice("配置已保存");
		} catch (e) {
			throw new Error(errorMessage(e));
		}
	};
	const saveEntity = async (value: Record<string, unknown>) => {
		if (!editor || !config) return;
		const { entity, row } = editor;
		const all = config[entity] as unknown as Record<string, unknown>[];
		if (!row && all.some((v) => v.id === value.id && (entity !== "scenes" || v.project_id === value.project_id)))
			throw new Error("标识已存在");
		const next = {
			...config,
			[entity]: row
				? all.map((v) => (v.id === row.id && (entity !== "scenes" || v.project_id === row.project_id) ? value : v))
				: [...all, value],
		};
		await commit(next);
		setEditor(undefined);
	};
	const removeEntity = async () => {
		if (!removal || !config) return;
		try {
			await commit({
				...config,
				[removal.entity]: config[removal.entity].filter(
					(v) => !(v.id === removal.id && (removal.entity !== "scenes" || (v as { project_id: string }).project_id === removal.project_id)),
				),
			});
			setRemoval(undefined);
		} catch (e) {
			setNotice(errorMessage(e));
		}
	};
	const copy = async (value: string) => {
		try {
			await navigator.clipboard.writeText(value);
			setCopied(true);
			setTimeout(() => setCopied(false), 2000);
		} catch {
			setNotice("无法访问剪贴板");
		}
	};
	const makeKey = async () => {
		if (!keyProject) return;
		try {
			const result = await createKey(keyProject).unwrap();
			setSecret(result.token);
		} catch (e) {
			setNotice(errorMessage(e));
		}
	};
	const exportConfig = () => {
		if (!config) return;
		const url = URL.createObjectURL(new Blob([JSON.stringify(config, null, 2)], { type: "application/json" }));
		const a = document.createElement("a");
		a.href = url;
		a.download = "llmhub-config.json";
		a.click();
		URL.revokeObjectURL(url);
	};
	const title = nav.find((v) => v.id === page)?.label ?? "总览";
	const usage = overview?.usage ?? [];
	const totals = usage.reduce(
		(a, v) => ({
			cost: a.cost + v.cost_usd,
			attempts: a.attempts + v.attempts,
			tokens: a.tokens + v.input_tokens + v.output_tokens,
			errors: a.errors + v.errors,
			reserved: a.reserved + v.reserved_usd,
		}),
		{ cost: 0, attempts: 0, tokens: 0, errors: 0, reserved: 0 },
	);
	const recent = (overview?.recent ?? []).filter((r) =>
		`${r.request_id} ${r.scene_id} ${r.model} ${r.project_id}`.toLowerCase().includes(search.toLowerCase()),
	);
	const requestTable = (
		<div className="table-wrap">
			<table>
				<thead>
					<tr>
						<th>请求 / 场景</th>
						<th>项目</th>
						<th>模型</th>
						<th>Token</th>
						<th>费用</th>
						<th>排队 / 总耗时</th>
						<th>状态</th>
					</tr>
				</thead>
				<tbody>
					{recent.map((r) => (
						<tr key={r.id} onClick={() => setSelected(r)} className="click-row" data-testid="hub-request-row">
							<td>
								<button
									className="text-button"
									onClick={(e) => {
										e.stopPropagation();
										setSelected(r);
									}}
								>
									{r.scene_id}
								</button>
								<small>{new Date(r.created_at).toLocaleString()}</small>
							</td>
							<td>{config?.projects.find((p) => p.id === r.project_id)?.name ?? r.project_id}</td>
							<td>
								<span className="mono">{r.model}</span>
								<small>{r.provider}</small>
							</td>
							<td>{number(r.input_tokens + r.output_tokens)}</td>
							<td>
								{usd(r.cost_usd)}
								{r.estimated && <small>估算</small>}
							</td>
							<td>
								{r.queue_ms} / {r.duration_ms} ms
							</td>
							<td>
								<span className={`status ${r.status === "success" ? "good" : r.status === "running" ? "neutral" : "bad"}`}>{r.status}</span>
							</td>
						</tr>
					))}
				</tbody>
			</table>
			{recent.length === 0 && <Empty label="暂无请求记录" />}
		</div>
	);
	return (
		<div className="shell">
			<aside className={mobile ? "sidebar open" : "sidebar"}>
				<div className="brand">
					<img src="/mark.svg" alt="" />
					<span>
						llm<span className="brand-accent">Hub</span>
					</span>
					<small>CONSOLE</small>
				</div>
				<div className="workspace">
					<span className="workspace-dot" />
					<span>Personal workspace</span>
				</div>
				<nav aria-label="主导航">
					{nav.map(({ id, label, icon: Icon }) => (
						<button
							className={page === id ? "nav-item active" : "nav-item"}
							onClick={() => navigate(id)}
							key={id}
							data-testid={`hub-nav-${id}`}
						>
							<Icon size={18} />
							<span>{label}</span>
							{id === "projects" && <small>{config?.projects.length ?? 0}</small>}
						</button>
					))}
				</nav>
				<div className="sidebar-bottom">
					<ShieldCheck size={16} />
					<span>Single instance · SQLite</span>
				</div>
			</aside>
			<main>
				<header className="topbar">
					<div>
						<button className="icon mobile-toggle" title="菜单" onClick={() => setMobile(!mobile)}>
							<Menu size={18} />
						</button>
						<span>Workspace</span>
						<span className="separator">/</span>
						<b>{title}</b>
					</div>
					<div>
						<span className="live-dot" />
						<span>{token && config ? "已连接" : "等待认证"}</span>
						{token && (
							<button className="icon" title="退出登录" aria-label="退出登录" onClick={logout} data-testid="hub-logout">
								<LogOut size={17} />
							</button>
						)}
					</div>
				</header>
				{!token || configQuery.isError ? (
					<section className="login">
						<img src="/mark.svg" alt="" />
						<h1>llmHub</h1>
						<form
							onSubmit={(e) => {
								e.preventDefault();
								dispatch(api.util.resetApiState());
								dispatch(authenticate(draftToken));
								setDraftToken("");
							}}
						>
							<label>
								管理员令牌
								<input
									type="password"
									autoComplete="off"
									required
									minLength={24}
									value={draftToken}
									onChange={(e) => setDraftToken(e.target.value)}
									data-testid="hub-admin-token"
								/>
							</label>
							{configQuery.isError && (
								<p className="error" role="alert">
									{errorMessage(configQuery.error)}
								</p>
							)}
							<button className="primary" data-testid="hub-login">
								<KeyRound size={16} />
								连接控制台
							</button>
						</form>
					</section>
				) : (
					<div className="content">
						<div className="page-heading">
							<div>
								<div className="eyebrow">LLMHUB / {page.toUpperCase()}</div>
								<h1>{title}</h1>
							</div>
							<div className="toolbar">
								<button
									className="icon"
									title="刷新数据"
									aria-label="刷新数据"
									onClick={() => {
										configQuery.refetch();
										overviewQuery.refetch();
										keysQuery.refetch();
										if (page === "profiles") pricingQuery.refetch();
									}}
									data-testid="hub-refresh"
								>
									<RefreshCw size={17} />
								</button>
								{["projects", "scenes", "profiles", "pools"].includes(page) && (
									<button className="primary" onClick={() => setEditor({ entity: page as Entity })} data-testid="hub-create">
										<Plus size={16} />
										新建
									</button>
								)}
							</div>
						</div>
						{overview?.demo && (
							<div className="notice" role="status">
								{page === "catalog"
									? "本地模拟环境 · 模型请求仅访问本机；价格与公告来自真实官网"
									: "本地模拟环境 · 数据来自模拟请求，不产生供应商费用"}
							</div>
						)}
						{notice && (
							<div className="notice" role="status">
								<span>{notice}</span>
								<button className="text-button" onClick={() => setNotice("")}>
									关闭
								</button>
							</div>
						)}
						{overviewQuery.isError && (
							<p className="error" role="alert">
								{errorMessage(overviewQuery.error)}
							</p>
						)}
						{configQuery.isLoading ? (
							<Empty label="正在载入配置…" />
						) : !config ? (
							<Empty label="配置不可用" />
						) : (
							<>
								{["overview", "usage", "requests"].includes(page) && (
									<div className="filters">
										<label>
											月份{" "}
											<input
												type="month"
												value={month}
												onChange={(e) => setMonth(e.target.value || localMonth())}
												data-testid="hub-month"
											/>
										</label>
										<label>
											项目{" "}
											<select value={project} onChange={(e) => setProject(e.target.value)} data-testid="hub-project-filter">
												<option value="">全部项目</option>
												{config.projects.map((p) => (
													<option value={p.id} key={p.id}>
														{p.name}
													</option>
												))}
											</select>
										</label>
										{page === "requests" && (
											<div className="search">
												<Search size={16} />
												<input
													placeholder="搜索请求、场景、模型"
													value={search}
													onChange={(e) => setSearch(e.target.value)}
													data-testid="hub-search"
												/>
											</div>
										)}
									</div>
								)}
								{page === "overview" && (
									<>
										<div className="metrics">
											{[
												{ label: "本月费用", value: usd(totals.cost), note: `预留 ${usd(totals.reserved)}` },
												{ label: "调用次数", value: number(totals.attempts), note: "包含重试与备用路由" },
												{ label: "Token 用量", value: number(totals.tokens), note: "输入 + 输出" },
												{
													label: "成功率",
													value: totals.attempts ? `${((1 - totals.errors / totals.attempts) * 100).toFixed(1)}%` : "—",
													note: `${totals.errors} 次非成功调用`,
												},
											].map((m) => (
												<div className="metric" key={m.label}>
													<label>{m.label}</label>
													<strong>{m.value}</strong>
													<small>{m.note}</small>
												</div>
											))}
										</div>
										<div className="overview-columns">
											<section>
												<div className="section-title">
													<h2>项目成本</h2>
													<button className="text-button" onClick={() => navigate("usage")}>
														查看明细
														<ArrowUpRight size={15} />
													</button>
												</div>
												<div className="cost-chart">
													{config.projects
														.filter((p) => !project || p.id === project)
														.map((p, i) => {
															const u = usage.find((v) => v.project_id === p.id);
															const cost = u?.cost_usd ?? 0;
															const max = Math.max(...usage.map((v) => v.cost_usd), 0.01);
															return (
																<div className="cost-row" key={p.id}>
																	<span>{p.name}</span>
																	<div className="bar-track">
																		<div
																			style={{
																				width: `${Math.max((cost / max) * 100, cost ? 1 : 0)}%`,
																				background: ["#43a578", "#478cc2", "#c49845", "#9776aa", "#bf6c76"][i % 5],
																			}}
																		/>
																	</div>
																	<b>{usd(cost)}</b>
																</div>
															);
														})}
												</div>
											</section>
											<section>
												<div className="section-title">
													<h2>共享队列</h2>
													<button className="text-button" onClick={() => navigate("pools")}>
														管理
														<ArrowUpRight size={15} />
													</button>
												</div>
												{(overview?.pools ?? []).map((p) => (
													<div className="queue-line" key={p.id}>
														<ListOrdered size={18} />
														<div>
															<b>{p.id}</b>
															<small>
																{p.active} 执行 · {p.queued} 等待
															</small>
														</div>
														<span className={`status ${p.cooldown_until ? "bad" : "good"}`}>{p.cooldown_until ? "冷却中" : "就绪"}</span>
													</div>
												))}
												{!overview?.pools.length && <Empty label="尚未配置限额池" />}
											</section>
										</div>
										<section className="requests-section">
											<div className="section-title">
												<h2>最近请求</h2>
												<button className="text-button" onClick={() => navigate("requests")}>
													全部记录
													<ArrowUpRight size={15} />
												</button>
											</div>
											{requestTable}
										</section>
									</>
								)}
								{page === "projects" && (
									<div className="table-wrap">
										<table>
											<thead>
												<tr>
													<th>项目</th>
													<th>月预算</th>
													<th>状态</th>
													<th>场景数</th>
													<th />
												</tr>
											</thead>
											<tbody>
												{config.projects.map((p) => (
													<tr key={p.id}>
														<td>
															<b>{p.name}</b>
															<small className="mono">{p.id}</small>
														</td>
														<td>{p.monthly_budget_usd ? usd(p.monthly_budget_usd) : "不限"}</td>
														<td>
															<span className={`status ${p.enabled ? "good" : "neutral"}`}>{p.enabled ? "启用" : "停用"}</span>
														</td>
														<td>{config.scenes.filter((s) => s.project_id === p.id).length}</td>
														<td>
															<Actions
																onEdit={() => setEditor({ entity: "projects", row: { ...p } })}
																onDelete={() => setRemoval({ entity: "projects", id: p.id })}
															/>
														</td>
													</tr>
												))}
											</tbody>
										</table>
										{!config.projects.length && <Empty label="暂无项目" />}
									</div>
								)}
								{page === "scenes" && (
									<div className="table-wrap">
										<table>
											<thead>
												<tr>
													<th>场景</th>
													<th>项目</th>
													<th>接口</th>
													<th>路由策略 / 候选</th>
													<th>超时 / 重试</th>
													<th />
												</tr>
											</thead>
											<tbody>
												{config.scenes.map((s) => (
													<tr key={`${s.project_id}/${s.id}`}>
														<td>
															<b>{s.name}</b>
															<small className="mono">scene/{s.id}</small>
														</td>
														<td>{s.project_id}</td>
														<td className="mono">{s.endpoint}</td>
														<td>
															<small>{s.routing_policy === "lowest_cost" ? "当前预估成本优先" : "配置顺序优先"}</small>
															{s.profiles.map((p, i) => (
																<span className="route-step" key={p}>
																	{i + 1}. {p}
																</span>
															))}
														</td>
														<td>
															{s.timeout_seconds}s / {s.retries}
														</td>
														<td>
															<Actions
																onEdit={() => setEditor({ entity: "scenes", row: { ...s } })}
																onDelete={() => setRemoval({ entity: "scenes", id: s.id, project_id: s.project_id })}
															/>
														</td>
													</tr>
												))}
											</tbody>
										</table>
										{!config.scenes.length && <Empty label="暂无场景" />}
									</div>
								)}
								{page === "catalog" && configQuery.data && (
									<CatalogView
										config={configQuery.data.config}
										version={configQuery.data.version}
										onSave={commit}
										refreshConfig={() => {
											void configQuery.refetch();
										}}
									/>
								)}
								{page === "profiles" && (
									<section>
										<div className="price-status" data-testid="hub-pricing-status">
											{pricingQuery.isError ? (
												<span className="error">当前价格读取失败</span>
											) : pricingQuery.data ? (
												<span>
													当前价格 · {new Date(pricingQuery.data.at).toLocaleString()} <small>USD / 百万 Token</small>
												</span>
											) : (
												<span>当前价格加载中</span>
											)}
										</div>
										<div className="table-wrap">
											<table>
												<thead>
													<tr>
														<th>Profile</th>
														<th>供应商 / 模型</th>
														<th>密钥 / 限额池</th>
														<th>当前输入 / 输出 USD / 1M</th>
														<th>图片 USD / 张</th>
														<th />
													</tr>
												</thead>
												<tbody>
													{config.profiles.map((p) => {
														const effective = pricingQuery.isError ? undefined : pricingQuery.data?.profiles.find((v) => v.id === p.id);
														const verification = pricingQuery.data?.verification.find((v) => v.id === p.id);
														return (
															<tr key={p.id}>
																<td>
																	<b>{p.id}</b>
																	<small>{p.follow_official ? "跟随官方 · 状态见官方模型与价格" : "手动价格"}</small>
																</td>
																<td>
																	{p.provider}
																	<small className="mono">{p.model}</small>
																</td>
																<td>
																	{p.key_name}
																	<small>{p.pool_id}</small>
																</td>
																<td data-testid={`hub-current-price-${p.id}`}>
																	{p.follow_official && verification?.status !== "verified" ? (
																		<span className="error">暂停派发 · {verification?.reason ?? "待验证"}</span>
																	) : effective ? (
																		<>
																			{usd(effective.input_usd_per_million)} / {usd(effective.output_usd_per_million)}
																			<small>
																				{effective.applied_price?.label} · {effective.applied_price?.timezone}
																			</small>
																			{effective.cached_input_usd_per_million !== undefined && (
																				<small>缓存输入 {usd(effective.cached_input_usd_per_million)}</small>
																			)}
																		</>
																	) : (
																		"待更新"
																	)}
																</td>
																<td>
																	{effective?.image_usd_per_image ? (
																		<>
																			{usd(effective.image_usd_per_image)}
																			<small>
																				{p.image_size} · {p.image_quality} · 估算
																			</small>
																		</>
																	) : (
																		"—"
																	)}
																</td>
																<td>
																	<Actions
																		onEdit={() => setEditor({ entity: "profiles", row: { ...p } })}
																		onDelete={() => setRemoval({ entity: "profiles", id: p.id })}
																	/>
																</td>
															</tr>
														);
													})}
												</tbody>
											</table>
											{!config.profiles.length && <Empty label="暂无 Profile" />}
										</div>
									</section>
								)}
								{page === "pools" && (
									<div className="pool-list">
										{config.pools.map((p) => {
											const stat = overview?.pools.find((v) => v.id === p.id);
											return (
												<section className="pool-item" key={p.id}>
													<div className="section-title">
														<h2>{p.id}</h2>
														<Actions
															onEdit={() => setEditor({ entity: "pools", row: { ...p } })}
															onDelete={() => setRemoval({ entity: "pools", id: p.id })}
														/>
													</div>
													<div className="pool-metrics">
														<div>
															<small>执行 / 并发</small>
															<b>
																{stat?.active ?? 0} / {p.concurrency}
															</b>
														</div>
														<div>
															<small>等待 / 容量</small>
															<b>
																{stat?.queued ?? 0} / {p.queue_size}
															</b>
														</div>
														<div>
															<small>60s 请求 / RPM</small>
															<b>
																{stat?.requests_in_window ?? 0} / {p.rpm || "∞"}
															</b>
														</div>
														<div>
															<small>60s Token / TPM</small>
															<b>
																{number(stat?.tokens_in_window ?? 0)} / {p.tpm ? number(p.tpm) : "∞"}
															</b>
														</div>
													</div>
													{stat?.cooldown_until && <p className="error">冷却至 {new Date(stat.cooldown_until).toLocaleString()}</p>}
												</section>
											);
										})}
										{!config.pools.length && <Empty label="暂无共享限额池" />}
									</div>
								)}
								{page === "requests" && requestTable}
								{page === "usage" && (
									<>
										<div className="table-wrap">
											<table>
												<thead>
													<tr>
														<th>项目</th>
														<th>调用</th>
														<th>输入 Token</th>
														<th>输出 Token</th>
														<th>费用 / 预留</th>
														<th>月预算</th>
													</tr>
												</thead>
												<tbody>
													{config.projects
														.filter((p) => !project || p.id === project)
														.map((p) => {
															const u = usage.find((v) => v.project_id === p.id);
															const ratio = p.monthly_budget_usd
																? Math.min(100, (((u?.cost_usd ?? 0) + (u?.reserved_usd ?? 0)) / p.monthly_budget_usd) * 100)
																: 0;
															return (
																<tr key={p.id}>
																	<td>
																		<b>{p.name}</b>
																	</td>
																	<td>{number(u?.attempts ?? 0)}</td>
																	<td>{number(u?.input_tokens ?? 0)}</td>
																	<td>{number(u?.output_tokens ?? 0)}</td>
																	<td>
																		{usd(u?.cost_usd ?? 0)}
																		<small>预留 {usd(u?.reserved_usd ?? 0)}</small>
																	</td>
																	<td>
																		{p.monthly_budget_usd ? (
																			<>
																				{usd(p.monthly_budget_usd)}
																				<progress value={ratio} max={100} />
																			</>
																		) : (
																			"不限"
																		)}
																	</td>
																</tr>
															);
														})}
												</tbody>
											</table>
										</div>
										<section className="requests-section">
											<div className="section-title">
												<h2>场景与模型明细</h2>
												<small>近 30 天记录 · {month}</small>
											</div>
											<div className="table-wrap">
												<table>
													<thead>
														<tr>
															<th>项目 / 场景</th>
															<th>Profile / 模型</th>
															<th>调用</th>
															<th>Token</th>
															<th>费用</th>
															<th>估算调用</th>
														</tr>
													</thead>
													<tbody>
														{(overview?.breakdown ?? []).map((b) => (
															<tr key={`${b.project_id}/${b.scene_id}/${b.profile_id}/${b.model}`}>
																<td>
																	{b.project_id}
																	<small>{b.scene_id}</small>
																</td>
																<td>
																	{b.profile_id}
																	<small>
																		{b.provider}/{b.model}
																	</small>
																</td>
																<td>{number(b.attempts)}</td>
																<td>{number(b.input_tokens + b.output_tokens)}</td>
																<td>{usd(b.cost_usd)}</td>
																<td>{b.estimated_attempts}</td>
															</tr>
														))}
													</tbody>
												</table>
												{!overview?.breakdown.length && <Empty label="暂无成本明细" />}
											</div>
										</section>
									</>
								)}
								{page === "keys" && (
									<>
										<div className="key-toolbar">
											<select
												aria-label="密钥所属项目"
												value={keyProject}
												onChange={(e) => setKeyProject(e.target.value)}
												data-testid="hub-key-project"
											>
												<option value="">选择项目</option>
												{config.projects
													.filter((p) => p.enabled)
													.map((p) => (
														<option value={p.id} key={p.id}>
															{p.name}
														</option>
													))}
											</select>
											<button
												className="primary"
												disabled={!keyProject || createState.isLoading}
												onClick={makeKey}
												data-testid="hub-key-create"
											>
												<Plus size={16} />
												签发密钥
											</button>
										</div>
										<div className="table-wrap">
											<table>
												<thead>
													<tr>
														<th>密钥前缀</th>
														<th>项目</th>
														<th>签发时间</th>
														<th>状态</th>
														<th />
													</tr>
												</thead>
												<tbody>
													{(keysQuery.data?.keys ?? []).map((k) => (
														<tr key={k.id}>
															<td className="mono">{k.prefix}…</td>
															<td>{k.project_id}</td>
															<td>{new Date(k.created_at).toLocaleString()}</td>
															<td>
																<span className={`status ${k.revoked ? "neutral" : "good"}`}>{k.revoked ? "已吊销" : "有效"}</span>
															</td>
															<td>
																{!k.revoked && (
																	<button
																		className="icon danger"
																		title="吊销密钥"
																		aria-label="吊销密钥"
																		onClick={() => setRevoke(k.id)}
																		data-testid="hub-key-revoke"
																	>
																		<Trash2 size={16} />
																	</button>
																)}
															</td>
														</tr>
													))}
												</tbody>
											</table>
											{!keysQuery.data?.keys.length && <Empty label="暂无项目密钥" />}
										</div>
									</>
								)}
								{page === "settings" && (
									<div className="settings-list">
										<section>
											<div className="section-title">
												<h2>
													<Server size={18} />
													Bifrost
												</h2>
												<a className="button" href={`${location.protocol}//${location.hostname}:8081`} target="_blank" rel="noreferrer">
													供应商控制台
													<ArrowUpRight size={16} />
												</a>
											</div>
											<dl>
												<dt>配置版本</dt>
												<dd>v{configQuery.data?.version}</dd>
												<dt>存储</dt>
												<dd>SQLite · WAL</dd>
												<dt>部署模式</dt>
												<dd>单实例 / 进程内 FIFO 调度</dd>
												<dt>请求内容</dt>
												<dd>不持久化 Prompt 与 Response</dd>
												<dt>请求明细保留</dt>
												<dd>30 天</dd>
												<dt>月度项目汇总</dt>
												<dd>长期保留</dd>
												<dt>供应商凭证</dt>
												<dd>Bifrost 管理</dd>
											</dl>
										</section>
										<section>
											<div className="section-title">
												<h2>配置备份</h2>
												<button onClick={exportConfig} data-testid="hub-export">
													<Download size={16} />
													导出 JSON
												</button>
											</div>
											<pre>{JSON.stringify(config, null, 2)}</pre>
										</section>
									</div>
								)}
							</>
						)}
						<footer className="page-footer">
							<span>llmHub · Powered by Bifrost</span>
							<span>费用单位 USD · 预算月份 UTC</span>
						</footer>
					</div>
				)}
			</main>
			{editor && config && (
				<Editor
					entity={editor.entity}
					row={editor.row}
					config={config}
					busy={saveState.isLoading}
					onClose={() => setEditor(undefined)}
					onSave={saveEntity}
				/>
			)}
			{removal && (
				<Modal title="删除配置" onClose={() => setRemoval(undefined)}>
					<p>删除 {removal.id}？仍被场景或 Profile 引用的配置需要先解除引用。</p>
					<footer>
						<button onClick={() => setRemoval(undefined)}>取消</button>
						<button className="danger" onClick={removeEntity} data-testid="hub-delete-confirm">
							删除
						</button>
					</footer>
				</Modal>
			)}
			{revoke && (
				<Modal title="吊销项目密钥" onClose={() => setRevoke(undefined)}>
					<p>吊销后，这个密钥将无法再提交请求。</p>
					<footer>
						<button onClick={() => setRevoke(undefined)}>取消</button>
						<button
							className="danger"
							onClick={async () => {
								try {
									await revokeKey(revoke).unwrap();
									setRevoke(undefined);
								} catch (e) {
									setNotice(errorMessage(e));
								}
							}}
							data-testid="hub-revoke-confirm"
						>
							吊销
						</button>
					</footer>
				</Modal>
			)}
			{secret && (
				<Modal title="项目密钥已签发" onClose={() => setSecret("")}>
					<p>明文仅在本次显示。</p>
					<div className="secret">
						<code data-testid="hub-key-secret">{secret}</code>
						<button className="icon" title="复制密钥" aria-label="复制密钥" onClick={() => copy(secret)}>
							{copied ? <Check size={17} /> : <Copy size={17} />}
						</button>
					</div>
					<footer>
						<button className="primary" onClick={() => setSecret("")} data-testid="hub-key-dismiss">
							完成
						</button>
					</footer>
				</Modal>
			)}
			{selected && (
				<Modal title="请求详情" onClose={() => setSelected(undefined)}>
					<dl className="request-detail">
						{selected.price_snapshot?.applied_price && (
							<>
								<dt>计价时段</dt>
								<dd>
									{selected.price_snapshot.applied_price.label} / {selected.price_snapshot.applied_price.window_id}
								</dd>
								<dt>计价时间</dt>
								<dd>{selected.price_snapshot.applied_price.priced_at}</dd>
								<dt>缓存输入 Token</dt>
								<dd>
									{selected.price_snapshot.applied_price.cache_usage_known
										? number(selected.price_snapshot.applied_price.cached_input_tokens)
										: "未返回缓存用量"}
								</dd>
							</>
						)}
						{Object.entries(selected).map(([key, value]) => (
							<React.Fragment key={key}>
								<dt>{key}</dt>
								<dd>{typeof value === "object" ? JSON.stringify(value, null, 2) : String(value)}</dd>
							</React.Fragment>
						))}
					</dl>
					<footer>
						<button onClick={() => copy(selected.request_id)}>
							<Copy size={16} />
							复制请求 ID
						</button>
						<button onClick={() => setSelected(undefined)}>关闭</button>
					</footer>
				</Modal>
			)}
		</div>
	);
}
function Empty({ label }: { label: string }) {
	return <div className="empty">{label}</div>;
}
function Actions({ onEdit, onDelete }: { onEdit: () => void; onDelete: () => void }) {
	return (
		<div className="actions">
			<button className="icon" title="编辑" aria-label="编辑" onClick={onEdit} data-testid="hub-edit">
				<Pencil size={16} />
			</button>
			<button className="icon" title="删除" aria-label="删除" onClick={onDelete} data-testid="hub-delete">
				<Trash2 size={16} />
			</button>
		</div>
	);
}
createRoot(document.getElementById("root")!).render(
	<Provider store={store}>
		<App />
	</Provider>,
);