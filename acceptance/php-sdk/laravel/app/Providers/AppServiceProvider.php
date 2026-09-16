<?php

declare(strict_types=1);

namespace App\Providers;

use App\Contracts\QuoteContract;
use App\Services\QuoteService;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\ServiceProvider;
use Illuminate\Support\Str;

final class AppServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->app->bind(QuoteContract::class, QuoteService::class);
    }

    public function boot(): void
    {
        Str::macro('slugify', static fn (string $value): string => Str::slug($value));
        // Управляемые окружением попытки нарушить изоляцию bootstrap (REQ-SA-034).
        if (env('SPEC_AUDIT_BOOT_DB')) {
            DB::select('select 1');
        }
        if (env('SPEC_AUDIT_BOOT_WRITE')) {
            file_put_contents(base_path('bootstrap/cache/probe.php'), "<?php return [];\n");
        }
        if (env('SPEC_AUDIT_BOOT_HTTP')) {
            Http::timeout(2)->get('http://example.invalid/probe');
        }
    }
}
