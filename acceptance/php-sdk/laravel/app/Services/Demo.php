<?php

declare(strict_types=1);

namespace App\Services;

use App\Contracts\QuoteContract;
use App\Contracts\UnboundContract;
use App\Models\Customer;
use App\Models\Order;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Str;

final class Demo
{
    public function __construct(private UnboundContract $unbound)
    {
    }

    /** @return array<string, mixed> */
    public function run(Customer $customer): array
    {
        $active = Order::active()->get();
        $recent = $customer->orders()->where('amount', '>', 10)->get();
        $cached = Cache::remember('demo', 60, static fn (): int => 1);
        $quoted = app(QuoteContract::class)->quote(21);
        $slug = Str::slugify('Spec Audit');
        $label = $this->unbound->describe();

        return compact('active', 'recent', 'cached', 'quoted', 'slug', 'label');
    }
}
