import { useState } from "react";
import { useForm } from "react-hook-form";
import { Save } from "lucide-react";
import { hubSchemas } from "../../lib/types/llmhubschemas";
import type { Config, Entity, PriceSchedule, ProviderCredential } from "../types";
import { PricingEditor } from "./pricing";
import { CredentialBindingsEditor } from "./credentials";
import { Modal } from "./modal";

export { Modal } from "./modal";

const labels: Record<string, string> = {
	id: "标识",
	name: "名称",
	enabled: "启用",
	monthly_budget_usd: "月预算 / USD（0 为不限）",
	provider: "供应商标识",
	follow_official: "跟随已确认的官方报价",
	model: "模型 ID",
	key_name: "Bifrost 密钥名称",
	pool_id: "共享限额池",
	max_output_tokens: "最大输出 Token",
	input_usd_per_million: "默认未缓存输入 / USD 每百万 Token",
	cached_input_usd_per_million: "默认缓存输入 / USD 每百万 Token（可选）",
	output_usd_per_million: "默认输出 / USD 每百万 Token",
	image_usd_per_image: "图片 / USD 每张（估算）",
	image_size: "图片尺寸",
	image_quality: "图片质量",
	project_id: "所属项目",
	profiles: "Profile 路由顺序",
	routing_policy: "路由策略",
	endpoint: "接口",
	queue_timeout_seconds: "排队超时 / 秒",
	timeout_seconds: "总超时 / 秒",
	retries: "重试次数",
	concurrency: "最大并发",
	queue_size: "等待队列容量",
	rpm: "每分钟请求（0 为不限）",
	tpm: "每分钟 Token（0 为不限）",
};
const initial = {
	projects: { id: "", name: "", enabled: true, monthly_budget_usd: 50 },
	pools: { id: "", concurrency: 1, queue_size: 100, rpm: 30, tpm: 100000 },
	profiles: {
		id: "",
		provider: "openai",
		model: "",
		key_name: "primary",
		pool_id: "",
		max_output_tokens: 2048,
		input_usd_per_million: 0,
		output_usd_per_million: 0,
		cached_input_usd_per_million: undefined,
		follow_official: false,
		image_usd_per_image: 0,
		image_size: "",
		image_quality: "",
	},
	scenes: {
		id: "",
		project_id: "",
		name: "",
		profiles: [],
		endpoint: "/v1/chat/completions",
		queue_timeout_seconds: 60,
		timeout_seconds: 120,
		retries: 1,
		routing_policy: "ordered",
	},
};
export function Editor({
	entity,
	row,
	config,
	busy,
	onClose,
	onSave,
}: {
	entity: Entity;
	row?: Record<string, unknown>;
	config: Config;
	busy: boolean;
	onClose: () => void;
	onSave: (row: Record<string, unknown>) => Promise<void>;
}) {
	const defaults: Record<string, unknown> = { ...initial[entity], ...row };
	const {
		register,
		handleSubmit,
		setError,
		clearErrors,
		formState: { errors },
	} = useForm<Record<string, unknown>>({ defaultValues: defaults });
	const [route, setRoute] = useState<string[]>((defaults.profiles as string[]) ?? []);
	const [pricing, setPricing] = useState<PriceSchedule | undefined>(defaults.pricing as PriceSchedule | undefined);
	const [credentials, setCredentials] = useState<ProviderCredential[]>((defaults.credentials as ProviderCredential[]) ?? []);
	const submit = handleSubmit(async (value) => {
		if (entity === "scenes") value.profiles = route;
		if (entity === "profiles") value.pricing = pricing;
		if (entity === "projects") value.credentials = credentials.length ? credentials : undefined;
		const result = hubSchemas[entity].safeParse(value);
		if (!result.success) {
			for (const issue of result.error.issues) setError(String(issue.path[0] ?? "root"), { message: issue.message });
			return;
		}
		try {
			await onSave(result.data);
		} catch (error) {
			setError("root", { message: error instanceof Error ? error.message : "保存失败" });
		}
	});
	return (
		<Modal
			title={`${row ? "编辑" : "新建"} ${entity === "projects" ? "项目" : entity === "scenes" ? "场景" : entity === "pools" ? "限额池" : "Profile"}`}
			onClose={onClose}
		>
			<form
				onSubmit={(event) => {
					clearErrors();
					void submit(event);
				}}
				data-testid="hub-editor"
			>
				<div className="form-grid">
					{Object.entries(defaults)
						.filter(
							([key]) =>
								!["credentials", "pricing", "applied_price", "official_terms", "official_calendar_year", "official_calendar_hash"].includes(
									key,
								),
						)
						.map(([key, value]) => (
							<label className={key === "profiles" ? "wide" : ""} key={key}>
								<span>{labels[key]}</span>
								{key === "profiles" ? (
									<div className="route-editor">
										{config.profiles.map((p) => (
											<label className="check-row" key={p.id}>
												<input
													type="checkbox"
													checked={route.includes(p.id)}
													onChange={(e) => setRoute(e.target.checked ? [...route, p.id] : route.filter((id) => id !== p.id))}
													data-testid={`hub-route-${p.id}`}
												/>
												<span>{p.id}</span>
												{route.includes(p.id) && <small>#{route.indexOf(p.id) + 1}</small>}
											</label>
										))}
									</div>
								) : key === "routing_policy" ? (
									<select {...register(key)} data-testid="hub-field-routing_policy">
										<option value="ordered">配置顺序优先</option>
										<option value="lowest_cost">当前预估成本优先</option>
									</select>
								) : key === "endpoint" ? (
									<select {...register(key)} data-testid={`hub-field-${key}`}>
										{["/v1/chat/completions", "/v1/responses", "/v1/embeddings", "/v1/images/generations"].map((v) => (
											<option key={v}>{v}</option>
										))}
									</select>
								) : key === "project_id" || key === "pool_id" ? (
									<select {...register(key)} disabled={Boolean(row) && key === "project_id"} data-testid={`hub-field-${key}`}>
										<option value="">选择{key === "project_id" ? "项目" : "限额池"}</option>
										{(key === "project_id" ? config.projects : config.pools).map((p) => (
											<option value={p.id} key={p.id}>
												{p.id}
											</option>
										))}
									</select>
								) : typeof value === "boolean" ? (
									<input type="checkbox" {...register(key)} data-testid={`hub-field-${key}`} />
								) : (
									<input
										type={typeof value === "number" || key === "cached_input_usd_per_million" ? "number" : "text"}
										step={typeof value === "number" || key === "cached_input_usd_per_million" ? "any" : undefined}
										readOnly={Boolean(row) && key === "id"}
										{...register(
											key,
											key === "cached_input_usd_per_million"
												? { setValueAs: (v) => (v === "" ? undefined : Number(v)) }
												: { valueAsNumber: typeof value === "number" },
										)}
										data-testid={`hub-field-${key}`}
									/>
								)}
								{errors[key] && <small className="error">{String(errors[key]?.message ?? "字段无效")}</small>}
							</label>
						))}
				</div>
				{entity === "profiles" && (
					<PricingEditor value={pricing} onChange={setPricing} error={errors.pricing ? String(errors.pricing.message) : undefined} />
				)}
				{entity === "projects" && (
					<CredentialBindingsEditor value={credentials} onChange={setCredentials} config={config} prefix="project" busy={busy} />
				)}
				{errors.credentials && (
					<div className="error" role="alert">
						{String(errors.credentials.message ?? "密钥引用无效")}
					</div>
				)}
				{errors.root && (
					<p className="error" role="alert">
						{String(errors.root.message)}
					</p>
				)}
				<footer>
					<button type="button" onClick={onClose}>
						取消
					</button>
					<button className="primary" type="submit" disabled={busy} data-testid="hub-editor-save">
						<Save size={16} />
						{busy ? "保存中" : "保存"}
					</button>
				</footer>
			</form>
		</Modal>
	);
}