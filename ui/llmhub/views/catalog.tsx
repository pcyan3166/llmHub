import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { hubCatalogSchema } from "../../lib/types/llmhubschemas";
import { RefreshCw, Check, Save, ExternalLink } from "lucide-react";
import { errorMessage, useCatalogQuery, useCheckCatalogMutation, useApplyCatalogMutation } from "../api";
import type { CatalogProfile, CatalogSettings, Config } from "../types";
import { Modal } from "./editor";

const date = (value: string) => (value && !value.startsWith("0001-") ? new Date(value).toLocaleString() : "尚未检查");
const rate = (value?: number) => (value === undefined ? "未列出" : `$${value}`);
const events: Record<string, string> = {
	new_model: "首次发现",
	price_changed: "报价变化",
	terms_changed: "计费条件变化",
	not_listed: "官网未再列出",
	applied: "自动更新",
	approved: "管理员采纳",
	check_failed: "检查失败",
	news_check_failed: "公告检查失败",
	news_changed: "官网公告变化",
	model_mentioned: "首次发现公告模型",
};

export function CatalogView({
	config,
	version,
	onSave,
	refreshConfig,
}: {
	config: Config;
	version: number;
	onSave: (value: Config) => Promise<void>;
	refreshConfig: () => void;
}) {
	const query = useCatalogQuery(undefined, { pollingInterval: 5000, skipPollingIfUnfocused: true });
	const [check, checking] = useCheckCatalogMutation();
	const [apply, applying] = useApplyCatalogMutation();
	const [notice, setNotice] = useState("");
	const [proposal, setProposal] = useState<CatalogProfile>();
	const [confirmed, setConfirmed] = useState(false);
	const [busy, setBusy] = useState(false);
	const {
		register,
		handleSubmit,
		watch,
		reset,
		setError,
		formState: { errors },
	} = useForm<CatalogSettings>({ defaultValues: config.catalog ?? { enabled: true, interval_minutes: 360 } });
	const enabled = watch("enabled");
	useEffect(() => {
		reset(config.catalog ?? { enabled: true, interval_minutes: 360 });
	}, [config.catalog?.enabled, config.catalog?.interval_minutes, reset]);
	useEffect(() => {
		if (query.data && !query.data.running) refreshConfig();
	}, [query.data?.running]);
	const data = query.data;
	return (
		<section className="catalog-view" data-testid="hub-catalog">
			<div className="section-title">
				<h2>官方检查</h2>
				<button
					className="icon"
					title="立即检查官网"
					aria-label="立即检查官网"
					data-testid="hub-catalog-check"
					disabled={checking.isLoading || data?.running}
					onClick={async () => {
						try {
							await check().unwrap();
							setNotice("官网检查已开始");
						} catch (e) {
							setNotice(errorMessage(e));
						}
					}}
				>
					<RefreshCw size={17} />
				</button>
			</div>
			<form
				className="catalog-controls"
				onSubmit={handleSubmit(async (value) => {
					const result = hubCatalogSchema.safeParse(value);
					if (!result.success) {
						setError("interval_minutes", { message: result.error.issues[0].message });
						return;
					}
					setBusy(true);
					try {
						await onSave({ ...config, catalog: result.data });
						setNotice("检查设置已保存");
					} catch (e) {
						setNotice(errorMessage(e));
					} finally {
						setBusy(false);
					}
				})}
			>
				<label className="check-row">
					<input type="checkbox" {...register("enabled")} data-testid="hub-catalog-enabled" />
					定时检查
				</label>
				<label>
					间隔 / 分钟
					<input
						type="number"
						min={15}
						max={10080}
						step={1}
						{...register("interval_minutes", { valueAsNumber: true })}
						data-testid="hub-catalog-interval"
					/>
				</label>
				<button type="submit" disabled={busy} data-testid="hub-catalog-save">
					<Save size={16} />
					保存
				</button>
				<span className="muted">{data?.running ? "检查中" : enabled ? "定时检查已启用" : "定时检查已暂停"}</span>
			</form>
			{errors.interval_minutes && (
				<p className="error" role="alert">
					{errors.interval_minutes.message}
				</p>
			)}
			{notice && <p role="status">{notice}</p>}
			{query.isError && (
				<p className="error" role="alert">
					{errorMessage(query.error)}
				</p>
			)}
			<div className="table-wrap">
				<table>
					<thead>
						<tr>
							<th>官方来源</th>
							<th>状态</th>
							<th>最近检查 / 成功验证</th>
							<th>下次检查</th>
						</tr>
					</thead>
					<tbody>
						{data?.sources.map((source) => {
							const stale =
								source.error ||
								source.verified_at.startsWith("0001-") ||
								new Date(data.at).getTime() - new Date(source.verified_at).getTime() > data.settings.interval_minutes * 120000;
							return (
								<tr key={source.provider} data-testid={`hub-catalog-source-${source.provider}`}>
									<td>
										<a href={source.url} target="_blank" rel="noreferrer">
											{source.provider}
											<ExternalLink size={13} />
										</a>
										<small>{source.quotes?.length ?? 0} 个报价模型</small>
										<small>
											<a href={source.news_url?.replace(/\.md$/, "")} target="_blank" rel="noreferrer">
												模型公告 <ExternalLink size={12} />
											</a>{" "}
											· {source.news_models?.length ?? 0} 个提及标识
										</small>
									</td>
									<td>
										<span className={stale ? "badge warn" : "badge good"}>{stale ? "未验证 / 过期" : "来源已验证"}</span>
										{source.error && <small className="error">{source.error}</small>}
										{source.news_error && <small className="error">公告检查失败：{source.news_error}</small>}
									</td>
									<td>
										{date(source.checked_at)}
										<small>验证：{date(source.verified_at)}</small>
										<small>公告：{date(source.news_checked_at)}</small>
									</td>
									<td>{enabled ? date(data.next_checks[source.provider]) : "已暂停"}</td>
								</tr>
							);
						})}
					</tbody>
				</table>
			</div>
			<div className="section-title">
				<h2>业务档案报价</h2>
			</div>
			<div className="table-wrap">
				<table>
					<thead>
						<tr>
							<th>Profile</th>
							<th>官网输入 / 输出 / 缓存 USD / 1M</th>
							<th>价格状态</th>
							<th />
						</tr>
					</thead>
					<tbody>
						{data?.profiles.map((p) => (
							<tr key={p.id} data-testid={`hub-catalog-profile-${p.id}`}>
								<td>{p.id}</td>
								<td>
									{p.quote ? (
										<>
											{rate(p.quote.input)} / {rate(p.quote.output)} / {rate(p.quote.cached)}
											{p.quote.peak && (
												<small>
													峰时 {rate(p.quote.peak.input)} / {rate(p.quote.peak.output)} / {rate(p.quote.peak.cached)}
												</small>
											)}
										</>
									) : (
										"未匹配"
									)}
								</td>
								<td>
									<span className={p.status === "verified" ? "badge good" : "badge warn"}>{p.reason}</span>
								</td>
								<td>
									{p.can_apply && (
										<button
											disabled={data.running}
											title="采纳报价并跟随官方调价"
											data-testid={`hub-catalog-apply-${p.id}`}
											onClick={() => {
												setConfirmed(false);
												setProposal(p);
											}}
										>
											<Check size={16} />
											采纳并跟随
										</button>
									)}
								</td>
							</tr>
						))}
					</tbody>
				</table>
			</div>
			<div className="section-title">
				<h2>官网模型报价</h2>
			</div>
			<div className="table-wrap">
				<table>
					<thead>
						<tr>
							<th>供应商 / 模型</th>
							<th>输入 / 输出 / 缓存 USD / 1M</th>
							<th>计费条件</th>
						</tr>
					</thead>
					<tbody>
						{data?.sources.flatMap((source) =>
							(source.quotes ?? []).map((q) => (
								<tr key={`${source.provider}/${q.name}`}>
									<td>
										{source.provider}
										<small className="mono">{q.name}</small>
									</td>
									<td>
										{rate(q.input)} / {rate(q.output)} / {rate(q.cached)}
										{q.peak && (
											<small>
												峰时 {rate(q.peak.input)} / {rate(q.peak.output)} / {rate(q.peak.cached)}
											</small>
										)}
									</td>
									<td>
										{q.conditions}
										{q.billing_details && (
											<details data-testid={`hub-catalog-details-${source.provider}-${q.name}`}>
												<summary>完整计费维度</summary>
												{Object.entries(q.billing_details).map(([dimension, price]) => (
													<small key={dimension}>
														{dimension}: {price}
													</small>
												))}
											</details>
										)}
									</td>
								</tr>
							)),
						)}
					</tbody>
				</table>
			</div>
			<div className="section-title">
				<h2>变更记录</h2>
			</div>
			<div className="table-wrap">
				<table data-testid="hub-catalog-events">
					<thead>
						<tr>
							<th>时间</th>
							<th>来源 / 模型</th>
							<th>变更</th>
							<th>输入 / 输出 USD / 1M</th>
						</tr>
					</thead>
					<tbody>
						{data?.events
							.slice(-100)
							.reverse()
							.map((e, i) => (
								<tr key={`${e.at}/${e.kind}/${e.model}/${i}`}>
									<td>{date(e.at)}</td>
									<td>
										{e.provider}
										<small>{e.model ?? e.profiles?.join(", ")}</small>
									</td>
									<td>{events[e.kind] ?? e.kind}</td>
									<td>
										{e.before && (
											<small>
												原 {rate(e.before.input)} / {rate(e.before.output)}
											</small>
										)}
										{e.after && (
											<>
												新 {rate(e.after.input)} / {rate(e.after.output)}
											</>
										)}
									</td>
								</tr>
							))}
					</tbody>
				</table>
			</div>
			{proposal && (
				<Modal title={`采纳官方报价 · ${proposal.id}`} onClose={() => setProposal(undefined)}>
					<form
						onSubmit={async (e) => {
							e.preventDefault();
							try {
								await apply({ profile_id: proposal.id, hash: proposal.hash!, version, confirm_conditions: confirmed }).unwrap();
								setProposal(undefined);
								setNotice("官方报价已采纳并启用跟随");
								refreshConfig();
							} catch (e) {
								setNotice(errorMessage(e));
							}
						}}
					>
						<p>{proposal.quote?.conditions}</p>
						{proposal.source_url && (
							<p>
								<a href={proposal.source_url.replace(/\.md$/, "")} target="_blank" rel="noreferrer">
									官方计费原文 <ExternalLink size={13} />
								</a>
							</p>
						)}
						<p>
							输入 {rate(proposal.quote?.input)} / 输出 {rate(proposal.quote?.output)} / 缓存 {rate(proposal.quote?.cached)}
						</p>
						{proposal.quote?.peak && (
							<p>
								峰时输入 {rate(proposal.quote.peak.input)} / 输出 {rate(proposal.quote.peak.output)} / 缓存{" "}
								{rate(proposal.quote.peak.cached)}
							</p>
						)}
						<label className="check-row">
							<input
								type="checkbox"
								required
								checked={confirmed}
								onChange={(e) => setConfirmed(e.target.checked)}
								data-testid="hub-catalog-confirm"
							/>
							已核对账户计费条件；峰谷模型的完整年度节假日日历已确认
						</label>
						{applying.isError && (
							<p role="alert" className="error">
								{errorMessage(applying.error)}
							</p>
						)}
						<footer>
							<button type="button" onClick={() => setProposal(undefined)}>
								取消
							</button>
							<button className="primary" type="submit" disabled={!confirmed || applying.isLoading} data-testid="hub-catalog-approve">
								<Check size={16} />
								确认采纳
							</button>
						</footer>
					</form>
				</Modal>
			)}
		</section>
	);
}