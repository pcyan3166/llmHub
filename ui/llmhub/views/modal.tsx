import { useEffect, useRef } from "react";
import { X } from "lucide-react";

export function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
	const ref = useRef<HTMLDialogElement>(null);
	useEffect(() => {
		ref.current?.showModal();
	}, []);
	return (
		<dialog ref={ref} onCancel={onClose} className="modal">
			<header>
				<h2>{title}</h2>
				<button type="button" className="icon" title="关闭" aria-label="关闭" onClick={onClose}>
					<X size={18} />
				</button>
			</header>
			{children}
		</dialog>
	);
}