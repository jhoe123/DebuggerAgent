import type { Product } from "../api";

function emoji(category: string): string {
  switch (category) {
    case "footwear":
      return "👟";
    case "bags":
      return "🎒";
    case "audio":
      return "🎧";
    case "accessories":
      return "🕶️";
    default:
      return "🛍️";
  }
}

export function ProductGrid({ products, loading }: { products: Product[]; loading: boolean }) {
  if (loading) return <p className="muted">Loading catalog…</p>;
  if (!products.length) return <p className="muted">Catalog unavailable — is the API running on :9090?</p>;
  return (
    <div className="product-grid">
      {products.map((p) => (
        <div className="product" key={p.id}>
          <div className="thumb" aria-hidden>
            {emoji(p.category)}
          </div>
          <div className="p-name">{p.name}</div>
          <div className="p-meta">
            <span className="cat">{p.category}</span>
            <span className="price">${p.price.toFixed(2)}</span>
          </div>
        </div>
      ))}
    </div>
  );
}
