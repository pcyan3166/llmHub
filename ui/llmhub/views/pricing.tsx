import { Plus, Trash2 } from "lucide-react";
import type { PriceSchedule, PriceWindow } from "../types";

export function PricingEditor({
	value,
	onChange,
	error,
}: {
	value?: PriceSchedule;
	onChange: (value: PriceSchedule | undefined) => void;
	error?: string;
}) {
	const update = (patch: Partial<PriceSchedule>) => value && onChange({ ...value, ...patch });
	const windowUpdate = (index: number, patch: Partial<PriceWindow>) =>
		value && update({ windows: value.windows.map((w, i) => (i === index ? { ...w, ...patch } : w)) });
	return (
		<fieldset className="pricing-editor" data-testid="hub-pricing-editor">
			<legend>时段价格</legend>
			<label className="check-row">
				<input
					type="checkbox"
					checked={Boolean(value)}
					data-testid="hub-pricing-enabled"
					onChange={(e) =>
						onChange(
							e.target.checked
								? {
										timezone: "UTC",
										calendar_timezone: "Asia/Shanghai",
										default_label: "谷时",
										excluded_dates: [],
										windows: [],
									}
								: undefined,
						)
					}
				/>
				<span>启用时段计价</span>
			</label>
			{value && (
				<>
					<div className="form-grid">
						<label>
							时段时区
							<input value={value.timezone} onChange={(e) => update({ timezone: e.target.value })} data-testid="hub-pricing-timezone" />
						</label>
						<label>
							节假日日历时区
							<input
								value={value.calendar_timezone ?? value.timezone}
								onChange={(e) => update({ calendar_timezone: e.target.value })}
								data-testid="hub-pricing-calendar-timezone"
							/>
						</label>
						<label>
							默认价格标签
							<input
								value={value.default_label}
								onChange={(e) => update({ default_label: e.target.value })}
								data-testid="hub-pricing-default-label"
							/>
						</label>
						<label>
							节假日 / 默认价格日期
							<textarea
								value={(value.excluded_dates ?? []).join("\n")}
								onChange={(e) => update({ excluded_dates: e.target.value.split(/[\s,;]+/).filter(Boolean) })}
								data-testid="hub-pricing-excluded-dates"
								placeholder="YYYY-MM-DD"
								rows={3}
							/>
						</label>
					</div>
					{value.windows.map((w, index) => (
						<fieldset className="price-window" key={index} data-testid={`hub-pricing-window-${index}`}>
							<legend>时段 {index + 1}</legend>
							<button
								type="button"
								className="icon price-remove"
								title="删除时段"
								aria-label="删除时段"
								data-testid={`hub-pricing-remove-${index}`}
								onClick={() => update({ windows: value.windows.filter((_, i) => i !== index) })}
							>
								<Trash2 size={16} />
							</button>
							<div className="form-grid">
								<label>
									标识
									<input
										value={w.id}
										onChange={(e) => windowUpdate(index, { id: e.target.value })}
										data-testid={`hub-pricing-id-${index}`}
									/>
								</label>
								<label>
									标签
									<input
										value={w.label}
										onChange={(e) => windowUpdate(index, { label: e.target.value })}
										data-testid={`hub-pricing-label-${index}`}
									/>
								</label>
								<label>
									开始
									<input
										value={w.start}
										onChange={(e) => windowUpdate(index, { start: e.target.value })}
										data-testid={`hub-pricing-start-${index}`}
										inputMode="numeric"
									/>
								</label>
								<label>
									结束
									<input
										value={w.end}
										onChange={(e) => windowUpdate(index, { end: e.target.value })}
										data-testid={`hub-pricing-end-${index}`}
										inputMode="numeric"
									/>
								</label>
								<label>
									未缓存输入 / USD 每百万 Token
									<input
										type="number"
										step="any"
										min="0"
										value={w.input_usd_per_million}
										onChange={(e) => windowUpdate(index, { input_usd_per_million: e.target.valueAsNumber })}
										data-testid={`hub-pricing-input-${index}`}
									/>
								</label>
								<label>
									输出 / USD 每百万 Token
									<input
										type="number"
										step="any"
										min="0"
										value={w.output_usd_per_million}
										onChange={(e) => windowUpdate(index, { output_usd_per_million: e.target.valueAsNumber })}
										data-testid={`hub-pricing-output-${index}`}
									/>
								</label>
								<label>
									缓存输入 / USD 每百万 Token
									<input
										type="number"
										step="any"
										min="0"
										value={w.cached_input_usd_per_million ?? ""}
										onChange={(e) =>
											windowUpdate(index, { cached_input_usd_per_million: e.target.value === "" ? undefined : e.target.valueAsNumber })
										}
										data-testid={`hub-pricing-cached-${index}`}
									/>
								</label>
								<label>
									图片 / USD 每张
									<input
										type="number"
										step="any"
										min="0"
										value={w.image_usd_per_image ?? 0}
										onChange={(e) => windowUpdate(index, { image_usd_per_image: e.target.valueAsNumber })}
										data-testid={`hub-pricing-image-${index}`}
									/>
								</label>
							</div>
							<div className="price-days">
								{["一", "二", "三", "四", "五", "六", "日"].map((label, i) => (
									<label className="check-row" key={i}>
										<input
											type="checkbox"
											checked={w.days.includes(i + 1)}
											onChange={(e) =>
												windowUpdate(index, { days: e.target.checked ? [...w.days, i + 1].sort() : w.days.filter((d) => d !== i + 1) })
											}
											data-testid={`hub-pricing-day-${index}-${i + 1}`}
										/>
										周{label}
									</label>
								))}
							</div>
						</fieldset>
					))}
					<button
						type="button"
						disabled={value.windows.length >= 32}
						data-testid="hub-pricing-add"
						onClick={() => {
							let n = value.windows.length + 1;
							while (value.windows.some((w) => w.id === `peak-${n}`)) n++;
							update({
								windows: [
									...value.windows,
									{
										id: `peak-${n}`,
										label: "峰时",
										days: [1, 2, 3, 4, 5],
										start: "01:00",
										end: "04:00",
										input_usd_per_million: 0,
										output_usd_per_million: 0,
									},
								],
							});
						}}
					>
						<Plus size={16} />
						添加时段
					</button>
				</>
			)}
			{error && (
				<p className="error" role="alert">
					{error}
				</p>
			)}
		</fieldset>
	);
}