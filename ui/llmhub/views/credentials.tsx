import { useRef, useState } from "react";
import { Plus, Trash2, Save } from "lucide-react";
import { hubCredentialsSchema } from "../../lib/types/llmhubschemas";
import type { Config, ProviderCredential } from "../types";
import { Modal } from "./modal";

export function CredentialBindingsEditor({
	value,
	onChange,
	config,
	prefix,
	busy = false,
	errors = {},
}: {
	value: ProviderCredential[];
	onChange: (rows: ProviderCredential[]) => void;
	config: Config;
	prefix: string;
	busy?: boolean;
	errors?: Record<string, string>;
}) {
	const rowIDs = useRef(value.map(() => crypto.randomUUID()));
	const update = (index: number, patch: Partial<ProviderCredential>) =>
		onChange(value.map((v, i) => (i === index ? { ...v, ...patch } : v)));
	return (
		<section className="credential-bindings">
			<div className="section-title">
				<h3>{prefix === "default" ? "默认平台 API Key" : "项目专用平台 API Key"}</h3>
				<button
					type="button"
					className="icon"
					title="添加平台 API Key"
					aria-label="添加平台 API Key"
					disabled={busy || value.length >= 64}
					onClick={() => {
						rowIDs.current.push(crypto.randomUUID());
						onChange([...value, { provider: "", key_name: "", pool_id: "" }]);
					}}
					data-testid={`hub-${prefix}-credential-add`}
				>
					<Plus size={17} />
				</button>
			</div>
			{value.length === 0 && (
				<div className="credential-empty">
					{prefix === "default" ? "尚未设置，使用已有模型配置中的平台密钥" : "未单独配置的平台使用默认 API Key"}
				</div>
			)}
			{value.map((row, index) => (
				<fieldset className="credential-row" key={rowIDs.current[index]} disabled={busy} data-testid={`hub-${prefix}-credential-${index}`}>
					<label>
						平台标识（如 openai）
						<input
							value={row.provider}
							onChange={(e) => update(index, { provider: e.target.value })}
							placeholder="openai"
							data-testid={`hub-${prefix}-credential-provider-${index}`}
							aria-invalid={Boolean(errors[`${index}.provider`])}
						/>
						{errors[`${index}.provider`] && (
							<small className="error" role="alert">
								第 {index + 1} 个平台的“平台标识”：{errors[`${index}.provider`]}
							</small>
						)}
					</label>
					<label>
						平台 API Key
						<input
							type="password"
							value={row.api_key ?? ""}
							onChange={(e) => update(index, { api_key: e.target.value })}
							placeholder={row.key_name ? "已配置，留空保持不变" : "粘贴该平台的 API Key"}
							autoComplete="off"
							data-testid={`hub-${prefix}-credential-key-${index}`}
							aria-invalid={Boolean(errors[`${index}.api_key`])}
						/>
						{errors[`${index}.api_key`] && (
							<small className="error" role="alert">
								第 {index + 1} 个平台的“平台 API Key”：{errors[`${index}.api_key`]}
							</small>
						)}
					</label>
					<label>
						限额池（可选）
						<select
							value={row.pool_id ?? ""}
							onChange={(e) => update(index, { pool_id: e.target.value })}
							data-testid={`hub-${prefix}-credential-pool-${index}`}
						>
							<option value="">自动使用默认限额设置</option>
							{config.pools.map((p) => (
								<option value={p.id} key={p.id}>
									{p.id}
								</option>
							))}
						</select>
						{errors[`${index}.pool_id`] && (
							<small className="error" role="alert">
								第 {index + 1} 个平台的“限额池”：{errors[`${index}.pool_id`]}
							</small>
						)}
					</label>
					<button
						type="button"
						className="icon"
						title="移除此平台 API Key 配置"
						aria-label="移除此平台 API Key 配置"
						onClick={() => {
							rowIDs.current.splice(index, 1);
							onChange(value.filter((_, i) => i !== index));
						}}
						data-testid={`hub-${prefix}-credential-remove-${index}`}
					>
						<Trash2 size={16} />
					</button>
				</fieldset>
			))}
		</section>
	);
}

export function DefaultCredentialsModal({
	config,
	busy,
	onSave,
	onClose,
}: {
	config: Config;
	busy: boolean;
	onSave: (rows: ProviderCredential[]) => Promise<void>;
	onClose: () => void;
}) {
	const [rows, setRows] = useState(config.default_credentials ?? []);
	const [error, setError] = useState("");
	const [fields, setFields] = useState<Record<string, string>>({});
	return (
		<Modal title="默认平台 API Key" onClose={onClose}>
			<form
				onSubmit={async (e) => {
					e.preventDefault();
					setError("");
					setFields({});
					const result = hubCredentialsSchema.safeParse(rows);
					if (!result.success) {
						const next: Record<string, string> = {};
						for (const issue of result.error.issues) {
							if (issue.path.length >= 2) next[issue.path.join(".")] = issue.message;
							else setError(`平台 API Key 配置：${issue.message}`);
						}
						setFields(next);
						return;
					}
					try {
						await onSave(result.data);
					} catch (err) {
						setError(err instanceof Error ? err.message : "保存失败");
					}
				}}
			>
				<CredentialBindingsEditor
					value={rows}
					onChange={(v) => {
						setRows(v);
						setError("");
						setFields({});
					}}
					config={config}
					prefix="default"
					busy={busy}
					errors={fields}
				/>
				{error && (
					<div className="error" role="alert">
						{error}
					</div>
				)}
				<footer>
					<button type="button" onClick={onClose}>
						取消
					</button>
					<button className="primary" type="submit" disabled={busy} data-testid="hub-default-credential-save">
						<Save size={16} />
						{busy ? "保存中" : "保存"}
					</button>
				</footer>
			</form>
		</Modal>
	);
}