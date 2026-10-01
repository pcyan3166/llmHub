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
}: {
	value: ProviderCredential[];
	onChange: (rows: ProviderCredential[]) => void;
	config: Config;
	prefix: string;
	busy?: boolean;
}) {
	const rowIDs = useRef(value.map(() => crypto.randomUUID()));
	const update = (index: number, patch: Partial<ProviderCredential>) =>
		onChange(value.map((v, i) => (i === index ? { ...v, ...patch } : v)));
	return (
		<section className="credential-bindings">
			<div className="section-title">
				<h3>{prefix === "default" ? "Default 上游密钥引用" : "项目专用上游密钥引用"}</h3>
				<button
					type="button"
					className="icon"
					title="添加供应商密钥引用"
					aria-label="添加供应商密钥引用"
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
					{prefix === "default" ? "未设置全局 default，沿用 Profile 默认值" : "未配置的供应商使用 default"}
				</div>
			)}
			{value.map((row, index) => (
				<fieldset className="credential-row" key={rowIDs.current[index]} disabled={busy} data-testid={`hub-${prefix}-credential-${index}`}>
					<label>
						供应商标识
						<input
							value={row.provider}
							onChange={(e) => update(index, { provider: e.target.value })}
							placeholder="openai"
							data-testid={`hub-${prefix}-credential-provider-${index}`}
						/>
					</label>
					<label>
						Bifrost 密钥名称
						<input
							value={row.key_name}
							onChange={(e) => update(index, { key_name: e.target.value })}
							placeholder="primary"
							autoComplete="off"
							data-testid={`hub-${prefix}-credential-key-${index}`}
						/>
					</label>
					<label>
						上游限额池
						<select
							value={row.pool_id}
							onChange={(e) => update(index, { pool_id: e.target.value })}
							data-testid={`hub-${prefix}-credential-pool-${index}`}
						>
							<option value="">选择限额池</option>
							{config.pools.map((p) => (
								<option value={p.id} key={p.id}>
									{p.id}
								</option>
							))}
						</select>
					</label>
					<button
						type="button"
						className="icon"
						title="移除密钥引用"
						aria-label="移除密钥引用"
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
	return (
		<Modal title="Default 上游密钥" onClose={onClose}>
			<form
				onSubmit={async (e) => {
					e.preventDefault();
					setError("");
					const result = hubCredentialsSchema.safeParse(rows);
					if (!result.success) {
						setError(result.error.issues.map((v) => v.message).join("；"));
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
					}}
					config={config}
					prefix="default"
					busy={busy}
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