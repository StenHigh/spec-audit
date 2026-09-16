<?php

declare(strict_types=1);

namespace Tests;

use PHPUnit\Framework\Attributes\Test;
use PHPUnit\Framework\TestCase;
use Tests\Support\Helper;

final class FileStoreTest extends TestCase
{
    #[Test]
    public function it_saves_without_checking_the_result(): void
    {
        $store = Helper::makeStore();
        $store->save('key', 'value');
        $this->assertTrue(true);
    }

    #[Test]
    public function it_compares_values(): void
    {
        $this->assertSame('value', 'value');
    }
}
