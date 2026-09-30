import { z } from "zod";

const id = z.string().regex(/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/, "使用字母、数字、点、短横线或下划线，最多 64 字符");
const money = z.number().min(0, "不能为负数").max(1000000);
export const hubProjectSchema = z.object({
	id,
	name: z.string().trim().min(1, "名称不能为空").max(128),
	enabled: z.boolean(),
	monthly_budget_usd: money,
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
		image_usd_per_image: money.optional(),
		image_size: z.string().max(32).optional(),
		image_quality: z.string().max(32).optional(),
	})
	.superRefine((v, ctx) => {
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
	})
	.refine((v) => v.queue_timeout_seconds <= v.timeout_seconds, { path: ["queue_timeout_seconds"], message: "排队超时不能超过请求总超时" });
export const hubSchemas = { projects: hubProjectSchema, profiles: hubProfileSchema, pools: hubPoolSchema, scenes: hubSceneSchema };