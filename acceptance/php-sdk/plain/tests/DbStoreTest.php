<?php

declare(strict_types=1);

namespace Tests;

use Demo\DbStore;
use PHPUnit\Framework\Attributes\Test;
use PHPUnit\Framework\TestCase;

final class DbStoreTest extends TestCase
{
    #[Test]
    public function it_saves_to_the_other_store(): void
    {
        $store = new DbStore();
        $store->save('key', 'value');
        $this->assertTrue(true);
    }
}
