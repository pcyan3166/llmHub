import { z } from "zod";

const id = z.string().regex(/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/, "使用字母、数字、点、短横线或下划线，最多 64 字符");
export const hubEstimateSchema = z.object({
	scene: z.string().min(1, "请选择业务场景"),
	request: z
		.string()
		.min(1, "请输入请求 JSON")
		.max(1048576)
		.refine((value) => {
			try {
				const body = JSON.parse(value);
				return typeof body === "object" && body !== null && !Array.isArray(body);
			} catch {
				return false;
			}
		}, "请求必须是有效的 JSON 对象"),
});
const money = z.number().min(0, "不能为负数").max(1000000);
const timezone = z.string().refine((v) => {
	try {
		new Intl.DateTimeFormat("en", { timeZone: v });
		return v !== "Local" && v.length > 0;
	} catch {
		return false;
	}
}, "请输入有效的 IANA 时区");
const clock = (end: boolean) =>
	z.string().regex(end ? /^(?:[01]\d|2[0-3]):[0-5]\d$|^24:00$/ : /^(?:[01]\d|2[0-3]):[0-5]\d$/, "时间须为 HH:MM，结束时间可用 24:00");
const pricing = z
	.object({
		timezone,
		calendar_timezone: timezone.optional(),
		default_label: z.string().min(1, "默认价格标签不能为空").max(64),
		excluded_dates: z
			.array(
				z
					.string()
					.refine(
						(v) => /^\d{4}-\d{2}-\d{2}$/.test(v) && !Number.isNaN(Date.parse(v)) && new Date(v).toISOString().slice(0, 10) === v,
						"节假日须为有效的 YYYY-MM-DD 日期",
					),
			)
			.max(730)
			.optional(),
		windows: z
			.array(
				z.object({
					id,
					label: z.string().min(1, "时段标签不能为空").max(64),
					days: z.array(z.number().int().min(1).max(7)).min(1, "至少选择一天").max(7),
					start: clock(false),
					end: clock(true),
					input_usd_per_million: money,
					output_usd_per_million: money,
					cached_input_usd_per_million: money.optional(),
					image_usd_per_image: money.optional(),
				}),
			)
			.max(32),
	})
	.superRefine((v, ctx) => {
		const occupied = new Set<number>();
		const ids = new Set<string>();
		const issue = (message: string) => ctx.addIssue({ code: "custom", message });
		if (new Set(v.excluded_dates).size !== (v.excluded_dates?.length ?? 0)) issue("节假日日期不能重复");
		for (const w of v.windows) {
			if (ids.has(w.id)) issue("时段标识不能重复");
			ids.add(w.id);
			if (new Set(w.days).size !== w.days.length) issue("时段日期不能重复");
			if ((w.cached_input_usd_per_million ?? 0) > w.input_usd_per_million) issue("缓存输入价格不能高于未缓存输入价格");
			const minute = (t: string) => Number(t.slice(0, 2)) * 60 + Number(t.slice(3));
			const start = minute(w.start),
				end = minute(w.end);
			if (start === end) issue("时段开始与结束时间不能相同");
			const duration = end > start ? end - start : 1440 - start + end;
			for (const day of w.days)
				for (let i = 0; i < duration; i++) {
					const slot = ((day - 1) * 1440 + start + i) % 10080;
					if (occupied.has(slot)) {
						issue("价格时段不能重叠（含跨午夜）");
						return;
					}
					occupied.add(slot);
				}
		}
	});
export const hubCredentialsSchema = z
	.array(
		z.object({
			provider: id,
			key_name: z
				.string()
				.min(1, "请输入 Bifrost 密钥名称")
				.max(128)
				.refine((v) => v.trim() === v && !/[\x00-\x1f\x7f]/.test(v), "密钥名称不能包含控制字符或首尾空格"),
			pool_id: id,
		}),
	)
	.max(64)
	.superRefine((rows, ctx) => {
		const seen = new Set<string>();
		rows.forEach((row, index) => {
			if (seen.has(row.provider)) ctx.addIssue({ code: "custom", path: [index, "provider"], message: "供应商不能重复" });
			seen.add(row.provider);
		});
	});
export const hubProjectSchema = z.object({
	id,
	name: z.string().trim().min(1, "名称不能为空").max(128),
	enabled: z.boolean(),
	monthly_budget_usd: money,
	credentials: hubCredentialsSchema.optional(),
});
export const hubCatalogSchema = z.object({
	enabled: z.boolean(),
	interval_minutes: z.number().int().min(15, "检查间隔至少 15 分钟").max(10080, "检查间隔不超过 7 天"),
});
export const hubPoolSchema = z.object({
	id,
	concurrency: z.number().int().min(1).max(256),
	queue_size: z.number().int().min(0).max(10000),
	rpm: z.number().int().min(0).max(1000000),
	tpm: z.number().int().min(0).max(1000000000),
});
export const hubProfileSchema = z
	.object({
		id,
		provider: id,
		model: z.string().min(1, "模型不能为空").max(256),
		key_name: z.string().min(1, "密钥名称不能为空").max(128),
		pool_id: id,
		max_output_tokens: z.number().int().min(1).max(1000000),
		input_usd_per_million: money,
		output_usd_per_million: money,
		cached_input_usd_per_million: money.optional(),
		pricing: pricing.optional(),
		follow_official: z.boolean().optional(),
		official_terms: z
			.string()
			.regex(/^[a-f0-9]{64}$/)
			.optional(),
		official_calendar_year: z.number().int().min(2000).max(2200).optional(),
		official_calendar_hash: z
			.string()
			.regex(/^[a-f0-9]{64}$/)
			.optional(),
		image_usd_per_image: money.optional(),
		image_size: z.string().max(32).optional(),
		image_quality: z.string().max(32).optional(),
	})
	.superRefine((v, ctx) => {
		if ((v.cached_input_usd_per_million ?? 0) > v.input_usd_per_million)
			ctx.addIssue({ code: "custom", path: ["cached_input_usd_per_million"], message: "缓存输入价格不能高于未缓存输入价格" });
		if ((v.image_usd_per_image ?? 0) > 0 && (!v.image_size || !v.image_quality))
			ctx.addIssue({ code: "custom", path: ["image_size"], message: "图片 Profile 必须配置尺寸和质量" });
	});
export const hubSceneSchema = z
	.object({
		id,
		project_id: id,
		name: z.string().min(1).max(128),
		profiles: z.array(id).min(1, "至少选择一个 Profile").max(8),
		endpoint: z.enum(["/v1/chat/completions", "/v1/responses", "/v1/embeddings", "/v1/images/generations"]),
		queue_timeout_seconds: z.number().int().min(1).max(900),
		timeout_seconds: z.number().int().min(1).max(900),
		retries: z.number().int().min(0).max(3),
		routing_policy: z.enum(["ordered", "lowest_cost"]).optional(),
	})
	.refine((v) => v.queue_timeout_seconds <= v.timeout_seconds, { path: ["queue_timeout_seconds"], message: "排队超时不能超过请求总超时" });
export const hubSchemas = { projects: hubProjectSchema, profiles: hubProfileSchema, pools: hubPoolSchema, scenes: hubSceneSchema };