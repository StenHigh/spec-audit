<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Services\QuoteService;
use PHPUnit\Framework\Attributes\Test;
use PHPUnit\Framework\TestCase;

final class DemoTest extends TestCase
{
    #[Test]
    public function it_quotes_double(): void
    {
        $service = new QuoteService();
        $this->assertSame(42, $service->quote(21));
    }
}
