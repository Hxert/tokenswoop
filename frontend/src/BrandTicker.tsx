import { brands } from './brands'

export default function BrandTicker() {
  return (
    <div
      className="ticker"
      role="region"
      aria-label={`AI and infrastructure brands: ${brands.map((brand) => brand.name).join(', ')}`}
    >
      <div className="ticker-track">
        {[0, 1, 2, 3].map((copy) => (
          <div
            className="ticker-group"
            key={copy}
            aria-hidden={copy > 0 ? true : undefined}
          >
            {brands.map((brand) => (
              <span
                className={`ticker-brand ticker-brand-${brand.id}`}
                key={brand.id}
              >
                <img
                  src={brand.src}
                  width="30"
                  height="30"
                  alt=""
                  aria-hidden="true"
                />
                <span>{brand.name}</span>
              </span>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}
