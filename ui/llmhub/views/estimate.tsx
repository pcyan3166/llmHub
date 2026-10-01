import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Calculator } from "lucide-react";
import { hubEstimateSchema } from "../../lib/types/llmhubschemas";
import { errorMessage, useEstimateMutation } from "../api";
import type { Config, CostPrediction, EstimateResponse } from "../types";
import { Modal } from "./editor";

const usd = (v: number) => `$${v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 6 })}`;
const confidence = { low: "低", medium: "中", high: "高", unknown: "未知" };

export function PredictionDetails({ prediction: p }: { prediction: CostPrediction }) {
	return (
		<dl className="request-detail prediction-detail" data-testid="hub-prediction-details">
			<dt>调用前预计费用</dt>
			<dd>{usd(p.cost_usd)}</dd>
			<dt>未命中预计费用</dt>
			<dd>{usd(p.uncached_usd)}</dd>
			<dt>预算保守预留</dt>
			<dd>{usd(p.reservation_usd)}</dd>
			<dt>预计输入 / 输出 Token</dt>
			<dd>
				{p.input_tokens} / {p.output_tokens}
			</dd>
			<dt>成本置信度 / 样本</dt>
			<dd>
				{confidence[p.confidence]} / {p.samples}
			</dd>
			<dt>缓存命中概率</dt>
			<dd>{p.cache_hit_probability === null ? "未知" : `${(p.cache_hit_probability * 100).toFixed(1)}%`}</dd>
			<dt>缓存置信度 / 近期样本</dt>
			<dd>
				{confidence[p.cache_confidence]} / {p.cache_samples}
			</dd>
			<dt>预计缓存输入 Token</dt>
			<dd>{p.expected_cached_tokens.toFixed(1)}</dd>
			<dt>预测依据</dt>
			<dd>{p.method === "exact_request_history" ? "完全相同请求的历史用量" : "无历史，使用保守上限"}</dd>
			<dt>缓存观测窗口</dt>
			<dd>{p.cache_horizon_seconds / 60} 分钟</dd>
			{p.error_usd !== undefined && (
				<>
					<dt>实际费用减预测费用</dt>
					<dd>{usd(p.error_usd)}</dd>
				</>
			)}
		</dl>
	);
}

export function EstimateModal({ config, onClose }: { config: Config; onClose: () => void }) {
	const [estimate, { isLoading }] = useEstimateMutation();
	const [result, setResult] = useState<EstimateResponse>();
	const [error, setError] = useState("");
	const scenes = config.scenes.filter((s) => config.projects.some((p) => p.id === s.project_id && p.enabled));
	const {
		register,
		handleSubmit,
		formState: { errors },
	} = useForm({
		resolver: zodResolver(hubEstimateSchema),
		defaultValues: { scene: "", request: '{\n  "messages": [{"role": "user", "content": ""}]\n}' },
	});
	return (
		<Modal title="调用前成本预估" onClose={onClose}>
			<form
				className="estimate-form"
				onChange={() => {
					setResult(undefined);
					setError("");
				}}
				onSubmit={handleSubmit(async (values) => {
					setError("");
					setResult(undefined);
					const scene = scenes.find((s) => `${s.project_id}/${s.id}` === values.scene);
					if (!scene) {
						setError("请选择有效场景");
						return;
					}
					try {
						setResult(await estimate({ project_id: scene.project_id, scene_id: scene.id, request: JSON.parse(values.request) }).unwrap());
					} catch (err) {
						setError(errorMessage(err));
					}
				})}
			>
				<label>
					业务场景
					<select {...register("scene")} data-testid="hub-estimate-scene" disabled={isLoading}>
						<option value="">选择场景</option>
						{scenes.map((s) => (
							<option key={`${s.project_id}/${s.id}`} value={`${s.project_id}/${s.id}`}>
								{config.projects.find((p) => p.id === s.project_id)?.name} / {s.name} ({s.endpoint})
							</option>
						))}
					</select>
				</label>
				{errors.scene && (
					<div role="alert" className="field-error">
						{errors.scene.message}
					</div>
				)}
				<label>
					请求 JSON
					<textarea {...register("request")} rows={9} spellCheck={false} data-testid="hub-estimate-request" disabled={isLoading} />
				</label>
				{errors.request && (
					<div role="alert" className="field-error">
						{errors.request.message}
					</div>
				)}
				{error && (
					<div role="alert" className="field-error">
						{error}
					</div>
				)}
				<footer>
					<button type="button" onClick={onClose} data-testid="hub-estimate-close">
						关闭
					</button>
					<button className="primary" type="submit" disabled={isLoading} data-testid="hub-estimate-submit">
						<Calculator size={16} />
						{isLoading ? "预估中" : "预估"}
					</button>
				</footer>
			</form>
			{result?.candidates.map((c) => (
				<section className="estimate-result" key={c.profile_id} data-testid={`hub-estimate-result-${c.profile_id}`}>
					<h3>{c.profile_id}</h3>
					{c.prediction ? <PredictionDetails prediction={c.prediction} /> : <div role="alert">{c.error}</div>}
				</section>
			))}
		</Modal>
	);
}