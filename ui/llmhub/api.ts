import { createApi, fetchBaseQuery } from "@reduxjs/toolkit/query/react";
import { configureStore, createSlice, PayloadAction } from "@reduxjs/toolkit";
import { setupListeners } from "@reduxjs/toolkit/query";
import type { ConfigResponse, Config, Key, Overview, PricingResponse, CatalogResponse } from "./types";

const session = createSlice({
	name: "session",
	initialState: { token: "" },
	reducers: {
		authenticate(state, action: PayloadAction<string>) {
			state.token = action.payload;
		},
	},
});
export const { authenticate } = session.actions;
export const api = createApi({
	reducerPath: "hubApi",
	baseQuery: fetchBaseQuery({
		baseUrl: "/api/llmhub/",
		timeout: 10000,
		prepareHeaders(headers, { getState }) {
			const { session } = getState() as { session: { token: string } };
			headers.set("Authorization", `Bearer ${session.token}`);
			return headers;
		},
	}),
	tagTypes: ["Config", "Keys", "Usage", "Catalog"],
	endpoints: (build) => ({
		config: build.query<ConfigResponse, void>({ query: () => "config", providesTags: ["Config"] }),
		pricing: build.query<PricingResponse, void>({ query: () => "pricing", providesTags: ["Config"] }),
		catalog: build.query<CatalogResponse, void>({ query: () => "catalog", providesTags: ["Catalog", "Config"] }),
		checkCatalog: build.mutation<{ running: boolean }, void>({
			query: () => ({ url: "catalog/check", method: "POST" }),
			invalidatesTags: ["Catalog"],
		}),
		applyCatalog: build.mutation<ConfigResponse, { profile_id: string; hash: string; version: number; confirm_conditions: boolean }>({
			query: (body) => ({ url: "catalog/apply", method: "POST", body }),
			invalidatesTags: ["Catalog", "Config", "Usage"],
		}),
		overview: build.query<Overview, { month: string; project: string }>({
			query: ({ month, project }) => `overview?month=${encodeURIComponent(month)}&project=${encodeURIComponent(project)}`,
			providesTags: ["Usage"],
		}),
		keys: build.query<{ keys: Key[] }, void>({ query: () => "keys", providesTags: ["Keys"] }),
		save: build.mutation<ConfigResponse, { config: Config; version: number }>({
			query: (body) => ({ url: "config", method: "PUT", body }),
			invalidatesTags: ["Config", "Usage"],
		}),
		createKey: build.mutation<{ key: Key; token: string }, string>({
			query: (project_id) => ({ url: "keys", method: "POST", body: { project_id } }),
			invalidatesTags: ["Keys"],
		}),
		revokeKey: build.mutation<void, string>({
			query: (id) => ({ url: `keys/${encodeURIComponent(id)}`, method: "DELETE" }),
			invalidatesTags: ["Keys"],
		}),
	}),
});
export const store = configureStore({
	reducer: { session: session.reducer, [api.reducerPath]: api.reducer },
	middleware: (getDefaultMiddleware) => getDefaultMiddleware().concat(api.middleware),
});
setupListeners(store.dispatch);
export const {
	useConfigQuery,
	usePricingQuery,
	useCatalogQuery,
	useCheckCatalogMutation,
	useApplyCatalogMutation,
	useOverviewQuery,
	useKeysQuery,
	useSaveMutation,
	useCreateKeyMutation,
	useRevokeKeyMutation,
} = api;
export function errorMessage(error: unknown): string {
	if (typeof error === "object" && error && "data" in error) {
		const data = error.data as { error?: { message?: string } };
		return data.error?.message ?? "请求失败";
	}
	return error instanceof Error ? error.message : "连接失败，请检查服务状态";
}